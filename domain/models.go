package domain

import "time"

// Document is metadata for an accepted signed ratingset document. The
// document bytes themselves live in content-addressed blob storage,
// keyed by Hash.
type Document struct {
	Userid       string
	Rev          int
	Hash         string
	Size         int
	ReceivedTime time.Time
	// Source is where the document came from: "" for a direct upload,
	// else the URL of the upstream server it was pulled from.
	Source string
}

// Artifact is a published snapshot or patch of a channel. A snapshot
// has Start == End, the epoch it materializes.
type Artifact struct {
	Channel string
	Kind    string
	Start   int
	End     int
	Parts   int
	Created time.Time
}

// ChannelChange records that, from Epoch on, a channel holds rev Rev of
// a user's document, or none if Rev is nil (the user was removed).
type ChannelChange struct {
	Userid string
	Rev    *int
}
