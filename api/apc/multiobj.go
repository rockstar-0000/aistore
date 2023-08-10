// Package apc: API messages and constants
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package apc

type (
	// List of object names _or_ a template specifying { Prefix, Regex, and/or Range }
	ListRange struct {
		Template string   `json:"template"`
		ObjNames []string `json:"objnames"`
	}

	// TODO: ContinueOnError vs. cmn.SupportedReactions - unify

	// ArchiveMsg contains the parameters (all except the destination bucket)
	// for archiving mutiple objects as one of the supported archive.FileExtensions types
	// at the specified (bucket) destination.
	// See also: api.PutApndArchArgs
	// --------------------  terminology   ---------------------
	// here and elsewhere "archive" is any (.tar, .tgz/.tar.gz, .zip, .tar.lz4) formatted object.
	ArchiveMsg struct {
		TxnUUID     string `json:"-"`        // internal use
		FromBckName string `json:"-"`        // ditto
		ArchName    string `json:"archname"` // one of the archive.FileExtensions
		Mime        string `json:"mime"`     // user-specified mime type (NOTE: takes precedence if defined)
		ListRange
		InclSrcBname    bool `json:"isbn"` // include source bucket name into the names of archived objects
		AppendIfExists  bool `json:"aate"` // adding a list or a range of objects to an existing archive
		ContinueOnError bool `json:"coer"` // on err, keep running arc xaction in a any given multi-object transaction
	}

	//  Multi-object copy & transform (see also: TCBMsg)
	TCObjsMsg struct {
		ListRange
		TxnUUID string `json:"-"`
		TCBMsg
		ContinueOnError bool `json:"coer"` // ditto; TODO above
	}
)

///////////////
// ListRange //
///////////////

// empty `ListRange{}` implies operating on an entire bucket ("all objects in the source bucket")

func (lrm *ListRange) IsList() bool      { return len(lrm.ObjNames) > 0 }
func (lrm *ListRange) HasTemplate() bool { return lrm.Template != "" }
