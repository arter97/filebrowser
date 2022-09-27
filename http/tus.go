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
	uploadDirName string
	handlers      map[uint]*tusd.UnroutedHandler
}

var mutex sync.Mutex

func NewTusHandler(store *storage.Storage, server *settings.Server) tusHandler {
	tusHandler := tusHandler{}
	tusHandler.store = store
	tusHandler.server = server
	tusHandler.uploadDirName = ".tmp_upload"
	tusHandler.handlers = make(map[uint]*tusd.UnroutedHandler)
	return tusHandler
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

	settings, err := th.store.Settings.Get()
	if err != nil {
		log.Fatalf("ERROR: couldn't get settings: %v\n", err)
		return
	}

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
		settings: settings,
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

	go th.handleTusFileUploaded(handler, d)

	return handler
}

func (th tusHandler) handleTusFileUploaded(handler *tusd.UnroutedHandler, d *data) {
	for {
		event := <-handler.CompleteUploads

		// Clean up only if an upload has been finalized
		if !event.Upload.IsFinal {
			continue
		}

		log.Printf("Final Upload of %s is finished. Moving file to target and cleaning up.\n", event.Upload.ID)

		uploadDir := filepath.Join(d.user.FullPath("/"), th.uploadDirName)
		uploadedFile := filepath.Join(uploadDir, event.Upload.ID)

		destination, ok := event.Upload.MetaData["destination"]
		if !ok {
			log.Fatalln("Could not process upload due to missing destination in metadata")
			continue
		}
		fullDestination := filepath.Join(d.user.FullPath("/"), destination)

		// Check if destination file already exists. If so, we require overwrite to be set
		if _, err := os.Stat(fullDestination); !errors.Is(err, os.ErrNotExist) {
			overwriteStr, ok := event.Upload.MetaData["overwrite"]
			if !ok {
				log.Fatalf("Could not process upload due to missing overwrite: %s\n", err)
				continue
			}
			overwrite, err := strconv.ParseBool(overwriteStr)
			if err != nil {
				log.Fatalf("Could not process upload due to error: %s\n", err)
				continue
			}
			if !overwrite {
				log.Fatalf("Overwrite is set to false while destination file %s exists. Skipping upload.\n", destination)
				continue
			}
			log.Printf("Overwriting existing destination file as overwrite is set to true: %s\n", destination)
		}

		// Move uploaded file from tmp upload folder to user folder
		if err := os.Rename(uploadedFile, fullDestination); err != nil {
			log.Fatalf("Could not move file from %s to %s: %s\n", uploadedFile, fullDestination, err)
			continue
		}

		// Remove uploaded tmp files for this finished upload
		for _, partialUpload := range append(event.Upload.PartialUploads, event.Upload.ID) {
			filesToDelete, err := filepath.Glob(filepath.Join(uploadDir, partialUpload+"*"))
			if err != nil {
				log.Fatalf("Could not find temp files to delete: %s\n", err)
				continue
			}
			log.Printf("Deleting temp files: %s\n", filesToDelete)
			for _, f := range filesToDelete {
				if err := os.Remove(f); err != nil {
					log.Fatalf("Could not delete upload file %s: %s\n", f, err)
					continue
				}
			}
		}

		// Delete folder basePath if it is empty
		if dir, err := ioutil.ReadDir(uploadDir); err == nil {
			if len(dir) == 0 {
				log.Println("Temp upload dir is empty. Deleting it..")
				if err := os.Remove(uploadDir); err != nil {
					log.Fatalf("Could not delete upload dir %s: %s\n", uploadDir, err)
					continue
				}
			}
		} else {
			log.Fatalf("Could not list files in base directory: %s\n", err)
		}
	}
}
