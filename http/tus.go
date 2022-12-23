package http

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/storage"

	"github.com/tus/tusd/pkg/filestore"
	tusd "github.com/tus/tusd/pkg/handler"

	"sync"

	"io/ioutil"
)

type tusHandler struct {
	store         *storage.Storage
	server        *settings.Server
	settings      *settings.Settings
	uploadDirName string
	handlers      map[uint]*tusd.UnroutedHandler
	apiPath       string
}

var mutex sync.Mutex

func NewTusHandler(store *storage.Storage, server *settings.Server, apiPath string) (tusHandler, error) {
	tusHandler := tusHandler{}
	tusHandler.store = store
	tusHandler.server = server
	tusHandler.uploadDirName = ".tmp_upload"
	tusHandler.handlers = make(map[uint]*tusd.UnroutedHandler)
	tusHandler.apiPath = apiPath

	var err error
	if tusHandler.settings, err = store.Settings.Get(); err != nil {
		return tusHandler, errors.New(fmt.Sprintf("Couldn't get settings: %s", err))
	}
	return tusHandler, nil
}

func getBasePathFromRequest(r *http.Request, apiPath string) (*url.URL, error) {
	// The tus protocol is designed to return a location header in its response, guiding the client to the correct endpoint.
	// However, in proxied environments, we cannot know the correct URL at compile time
	// We therefore make use of the Request-URI as sent by the client (https://www.w3.org/Protocols/rfc2616/rfc2616-sec5.html)
	// Since we know this URI always contains the api path "/api/tus" we configured, we can form the basePath for tus accordingly
	// In case this is a scheme-less URI, we prepend the origin header to form a full URL that has initially been requested
	idx := strings.Index(r.RequestURI, apiPath)
	if idx < 0 {
		return nil, fmt.Errorf("Expected URI to contain " + apiPath)
	}

	basePath, err := url.Parse(r.RequestURI[:(idx + len(apiPath))])
	if err != nil {
		return nil, err
	}
	if len(basePath.Scheme) != 0 {
		return basePath, nil
	}

	origin, ok := r.Header["Origin"]
	if !ok || len(origin) == 0 || len(origin[0]) == 0 {
		return nil, fmt.Errorf("Client sent a request to a relative tus URL. " +
			"Expected Origin header to be set in this case to form an absolute URL.")
	}
	parsedOrigin, err := url.Parse(origin[0])
	if err != nil {
		return nil, err
	}
	return parsedOrigin.ResolveReference(basePath), nil
}

func (th tusHandler) getOrCreateTusHandler(d *data, r *http.Request) (*tusd.UnroutedHandler, error) {
	handler, ok := th.handlers[d.user.ID]
	if !ok {
		log.Printf("Creating tus handler for user %s\n", d.user.Username)
		basePath, err := getBasePathFromRequest(r, th.apiPath)
		if err != nil {
			return nil, err
		}
		handler = th.createTusHandler(d, basePath.String())
		th.handlers[d.user.ID] = handler
	}

	return handler, nil
}

func (th tusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	code, err := withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		// Create a new tus handler for current user if it doesn't exist yet
		// Use a mutex to make sure only one tus handler is created for each user
		mutex.Lock()
		handler, err := th.getOrCreateTusHandler(d, r)
		mutex.Unlock()

		if err != nil {
			return 400, err
		}

		// Create upload directory for each request
		uploadDir := filepath.Join(d.user.FullPath("/"), ".tmp_upload")
		if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
			return http.StatusInternalServerError, err
		}

		switch r.Method {
		case "POST":
			handler.PostFile(w, r)
		case "HEAD":
			handler.HeadFile(w, r)
		case "PATCH":
			handler.PatchFile(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}

		// Isn't used
		return 201, nil
	})(w, r, &data{
		store:    th.store,
		settings: th.settings,
		server:   th.server,
	})

	if err != nil {
		http.Error(w, err.Error(), code)
	} else if code >= 400 {
		http.Error(w, "", code)
	}
}

func (th tusHandler) createTusHandler(d *data, basePath string) *tusd.UnroutedHandler {
	uploadDir := filepath.Join(d.user.FullPath("/"), th.uploadDirName)
	tusStore := filestore.FileStore{
		Path: uploadDir,
	}
	composer := tusd.NewStoreComposer()
	tusStore.UseIn(composer)

	handler, err := tusd.NewUnroutedHandler(tusd.Config{
		BasePath:              basePath,
		StoreComposer:         composer,
		NotifyCompleteUploads: true,
	})
	if err != nil {
		panic(fmt.Errorf("Unable to create handler: %s", err))
	}

	// Goroutine to handle completed uploads
	go func() {
		for {
			event := <-handler.CompleteUploads

			if err := th.handleTusFileUploaded(handler, d, event); err != nil {
				log.Printf("ERROR: couldn't handle completed upload: %s\n", err)
			}
		}
	}()

	return handler
}

func readMetadata(metadata tusd.MetaData, field string) (string, error) {
	if value, ok := metadata[field]; ok {
		return value, nil
	} else {
		return "", errors.New(fmt.Sprintf("Metadata field %s not found in upload request", field))
	}
}

func (th tusHandler) handleTusFileUploaded(handler *tusd.UnroutedHandler, d *data, event tusd.HookEvent) error {
	// Clean up only if an upload has been finalized
	if !event.Upload.IsFinal {
		return nil
	}

	filename, err := readMetadata(event.Upload.MetaData, "filename")
	if err != nil {
		return err
	}
	destination, err := readMetadata(event.Upload.MetaData, "destination")
	if err != nil {
		return err
	}
	overwriteStr, err := readMetadata(event.Upload.MetaData, "overwrite")
	if err != nil {
		return err
	}
	uploadDir := filepath.Join(d.user.FullPath("/"), th.uploadDirName)
	uploadedFile := filepath.Join(uploadDir, event.Upload.ID)
	fullDestination := filepath.Join(d.user.FullPath("/"), destination)

	log.Printf("Upload of %s (%s) is finished. Moving file to destination (%s) "+
		"and cleaning up temporary files.\n", filename, uploadedFile, fullDestination)

	// Check if destination file already exists. If so, we require overwrite to be set
	if _, err := os.Stat(fullDestination); !errors.Is(err, os.ErrNotExist) {
		if overwrite, err := strconv.ParseBool(overwriteStr); err != nil {
			return err
		} else if !overwrite {
			return fmt.Errorf("Overwrite is set to false while destination file %s exists. Skipping upload.\n", destination)
		}
	}

	// Move uploaded file from tmp upload folder to user folder
	if err := os.Rename(uploadedFile, fullDestination); err != nil {
		return err
	}

	// Remove uploaded tmp files for finished upload (.info objects are created and need to be removed, too))
	for _, partialUpload := range append(event.Upload.PartialUploads, event.Upload.ID) {
		filesToDelete, err := filepath.Glob(filepath.Join(uploadDir, partialUpload+"*"))
		if err != nil {
			return err
		}
		for _, f := range filesToDelete {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}

	// Delete folder basePath if it is empty
	dir, err := ioutil.ReadDir(uploadDir)
	if err != nil {
		return err
	}

	if len(dir) == 0 {
		// os.Remove won't remove non-empty folders in case of race condition
		if err := os.Remove(uploadDir); err != nil {
			return err
		}
	}

	return nil
}
