package http

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

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
}

var mutex sync.Mutex

func NewTusHandler(store *storage.Storage, server *settings.Server) (tusHandler, error) {
	tusHandler := tusHandler{}
	tusHandler.store = store
	tusHandler.server = server
	tusHandler.uploadDirName = ".tmp_upload"
	tusHandler.handlers = make(map[uint]*tusd.UnroutedHandler)

	var err error
	if tusHandler.settings, err = store.Settings.Get(); err != nil {
		return tusHandler, errors.New(fmt.Sprintf("Couldn't get settings: %s", err))
	}
	return tusHandler, nil
}

func (th tusHandler) getOrCreateTusHandler(d *data) *tusd.UnroutedHandler {
	if handler, ok := th.handlers[d.user.ID]; !ok {
		log.Printf("Creating tus handler for user %s\n", d.user.Username)
		handler = th.createTusHandler(d)
		th.handlers[d.user.ID] = handler
		return handler
	} else {
		return handler
	}
}

func (th tusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	code, err := withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		// Create a new tus handler for current user if it doesn't exist yet
		// Use a mutex to make sure only one tus handler is created for each user
		mutex.Lock()
		handler := th.getOrCreateTusHandler(d)
		mutex.Unlock()

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

func (th tusHandler) createTusHandler(d *data) *tusd.UnroutedHandler {
	uploadDir := filepath.Join(d.user.FullPath("/"), th.uploadDirName)
	tusStore := filestore.FileStore{
		Path: uploadDir,
	}
	composer := tusd.NewStoreComposer()
	tusStore.UseIn(composer)

	handler, err := tusd.NewUnroutedHandler(tusd.Config{
		BasePath:              "/api/tus/",
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
		if filesToDelete, err := filepath.Glob(filepath.Join(uploadDir, partialUpload+"*")); err != nil {
			return err
		} else {
			for _, f := range filesToDelete {
				if err := os.Remove(f); err != nil {
					return err
				}
			}
		}
	}

	// Delete folder basePath if it is empty
	if dir, err := ioutil.ReadDir(uploadDir); err != nil {
		return err
	} else if len(dir) == 0 {
		// os.Remove won't remove non-empty folders in case of race condition
		if err := os.Remove(uploadDir); err != nil {
			return err
		}
	}

	return nil
}
