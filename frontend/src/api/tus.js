import * as tus from "tus-js-client";
import { tusEndpoint } from "@/utils/constants";
import store from "@/store";
import { removePrefix } from "./utils";
import { settings } from ".";

export async function upload(url, content = "", overwrite = false, onupload) {
  const tusSettings = await getTusSettings();

  return new Promise((resolve, reject) => {
    var upload = new tus.Upload(content, {
      endpoint: tusEndpoint,
      chunkSize: tusSettings.chunkSize,
      retryDelays: computeRetryDelays(tusSettings),
      parallelUploads: tusSettings.parallelUploads || 1,
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

function computeRetryDelays(tusSettings) {
  if (!tusSettings.retryCount || tusSettings.retryCount < 1) {
    // Disable retries altogether
    return null;
  }
  // The tus client expects our retries as an array with computed backoffs
  // E.g.: [0, 3000, 5000, 10000, 20000]
  return Array.apply(null, { length: tusSettings.retryCount }).map(
    (_, idx) => (idx + 1) * tusSettings.retryBaseDelay * tusSettings.retryBackoff
  );
}

export async function useTus(content) {
  if (!isTusSupported() || !(content instanceof Blob)) {
    return false;
  }
  const tusSettings = await getTusSettings();
  // use tus if tus uploads are enabled and the content's size is larger than chunkSize
  return tusSettings.enabled === true && content.size > tusSettings.chunkSize;
}

// Temporarily store the tus settings stored in the backend
// Thus, we won't need to fetch the settings every time we upload a file
var temporaryTusSettings = null;

async function getTusSettings() {
  if (temporaryTusSettings) {
    return temporaryTusSettings;
  }
  temporaryTusSettings = await settings.getTusSettings();
  return temporaryTusSettings;
}

function isTusSupported() {
  return tus.isSupported === true;
}
