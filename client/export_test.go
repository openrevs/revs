package client

// SetBlobBatch sets how many documents, or bytes of them, a sync stores
// at once, until the returned function restores the defaults.
func SetBlobBatch(count, bytes int) (restore func()) {
	c, b := blobBatchCount, blobBatchBytes
	blobBatchCount, blobBatchBytes = count, bytes
	return func() { blobBatchCount, blobBatchBytes = c, b }
}
