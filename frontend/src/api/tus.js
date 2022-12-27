import * as tus from "tus-js-client";
import { tusEndpoint } from "@/utils/constants";
import store from "@/store";
import { removePrefix } from "./utils";
import { settings } from ".";

// Temporarily store the tus settings stored in the backend
// Thus, we won't need to fetch the settings every time we upload a file
var temporaryTusSettings = null;

// Make following configurable by envs?
const parallelUploads = 3;
const retryDelays = [0, 3000, 5000, 10000, 20000];

export async function upload(url, content = "", overwrite = false, onupload) {
  const tusSettings = await getTusSettings();
  return new Promise((resolve, reject) => {
    var upload = new tus.Upload(content, {
      endpoint: tusEndpoint,
      chunkSize: tusSettings.chunkSize,
      retryDelays: retryDelays,
      parallelUploads: parallelUploads,
      metadata: {
        filename: content.name,
        filetype: content.type,
        overwrite: overwrite.toString(),
        // url is URI encoded and needs to be decoded for metadata first
        destination: decodeURIComponent(removePrefix(url)),
      },
      headers: {
        "X-Auth": store.state.jwt,
      },
      onError: function (error) {
        reject("Upload failed: " + error);
      },
      onProgress: function (bytesUploaded) {
        // Emulate ProgressEvent.loaded which is used by calling functions
        // loaded is specified in bytes (https://developer.mozilla.org/en-US/docs/Web/API/ProgressEvent/loaded)
        if (typeof onupload === "function") {
          onupload({ loaded: bytesUploaded });
        }
      },
      onSuccess: function () {
        resolve();
      },
    });

    upload.findPreviousUploads().then(function (previousUploads) {
      if (previousUploads.length) {
        upload.resumeFromPreviousUpload(previousUploads[0]);
      }
      upload.start();
    });
  });
}

export async function useTus(content) {
  if (!isTusSupported() || !content instanceof Blob) {
    return false;
  }
  const tusSettings = await getTusSettings();
  // use tus if tus uploads are enabled and the content's size is larger than chunkSize
  const useTus =
    tusSettings.enabled === true &&
    content.size > tusSettings.chunkSize;
  return useTus;
}

async function getTusSettings() {
  if (temporaryTusSettings) {
    return temporaryTusSettings;
  }
  const fbSettings = await settings.get();
  temporaryTusSettings = fbSettings.tus;
  return temporaryTusSettings;
}

function isTusSupported() {
  return tus.isSupported === true;
}
