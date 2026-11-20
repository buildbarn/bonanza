package object

import (
	"time"
)

// Store for objects, which is accessible for reading (downloading) and
// writing (uploading).
type Store[TReference any, TLease any] interface {
	Downloader[TReference]
	Uploader[TReference, TLease]
}

// NewStore is a helper function for creating a Store that is backed by
// separate instances of Downloader and Uploader.
func NewStore[TReference, TLease any](downloader Downloader[TReference], uploader Uploader[TReference, TLease]) Store[TReference, TLease] {
	return struct {
		Downloader[TReference]
		Uploader[TReference, TLease]
	}{
		Downloader: downloader,
		Uploader:   uploader,
	}
}

// StoreForTesting is an instantiation of Store for generating mocks to
// be used by tests.
type StoreForTesting Store[GlobalReference, any]

// FlatStoreForTesting is an instantiation of Store for generating mocks
// to be used by tests that use flat references.
type FlatStoreForTesting Store[FlatReference, time.Time]
