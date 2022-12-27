package settings

const DefaultChunkSize = 20 * 1000 * 1000 // 20MB

// Tus contains the tus.io settings of the app.
type Tus struct {
	Enabled   bool  `json:"enabled"`
	ChunkSize int64 `json:"chunkSize"`
}
