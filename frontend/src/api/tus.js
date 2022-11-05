import * as tus from "tus-js-client";
import { tusEndpoint } from "@/utils/constants";
import store from "@/store";
import { removePrefix } from "./utils";

// Make following configurable by envs?
export const chunkSize = 20 * 1000 * 1000;
const parallelUploads = 3;
const retryDelays = [0, 3000, 5000, 10000, 20000];

export async function upload(url, content = "", overwrite = false, onupload) {
  return new Promise((resolve, reject) => {
    var upload = new tus.Upload(content, {
      endpoint: tusEndpoint,
      chunkSize: chunkSize,
      retryDelays: retryDelays,
      parallelUploads: parallelUploads,
      metadata: {
        filename: content.name,
        filetype: content.type,
        overwrite: overwrite.toString(),
        // url is URI encoded and needs to be for metadata first
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
