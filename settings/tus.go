package settings

const DefaultTusChunkSize = 20 * 1000 * 1000 // 20MB
const DefaultTusParallelUploads = 3
const DefaultTusRetryCount = 3
const DefaultTusRetryBaseDelay = 1000
const DefaultTusRetryBackoff = 2

// Tus contains the tus.io settings of the app.
type Tus struct {
	Enabled         bool    `json:"enabled"`
	ChunkSize       uint64  `json:"chunkSize"`
	ParallelUploads uint8   `json:"parallelUploads"`
	RetryCount      uint16  `json:"retryCount"`
	RetryBaseDelay  uint64  `json:"retryBaseDelay"`
	RetryBackoff    float32 `json:"retryBackoff"`
}
