// Package meta: cluster-level metadata
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package meta

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
)

type Bck cmn.Bck

func NewBck(name, provider string, ns cmn.Ns, optProps ...*cmn.Bprops) *Bck {
	var err error
	provider, err = cmn.NormalizeProvider(provider)
	debug.AssertNoErr(err)
	bck := &Bck{Name: name, Provider: provider, Ns: ns}
	if len(optProps) > 0 {
		bck.Props = optProps[0]
		debug.Assert(bck.Props != nil)
	}
	return bck
}

// clone (*meta.Bck | *cmn.Bck) <=> (cmn.Bck | meta.Bck) respectively
func (b *Bck) Clone() cmn.Bck { return cmn.Bck(*b) }

func CloneBck(bck *cmn.Bck) *Bck {
	b := *bck
	normp, err := cmn.NormalizeProvider(bck.Provider)
	debug.Assert(err == nil, bck.Provider)
	b.Provider = normp
	return (*Bck)(&b)
}

// cast *meta.Bck => *cmn.Bck
func (b *Bck) Bucket() *cmn.Bck { return (*cmn.Bck)(b) }

//
// inline delegations => cmn.Bck
//

func (b *Bck) IsAIS() bool                  { return (*cmn.Bck)(b).IsAIS() }
func (b *Bck) HasProvider() bool            { return (*cmn.Bck)(b).HasProvider() }
func (b *Bck) IsHT() bool                   { return (*cmn.Bck)(b).IsHT() }
func (b *Bck) IsCloud() bool                { return (*cmn.Bck)(b).IsCloud() }
func (b *Bck) IsRemote() bool               { return (*cmn.Bck)(b).IsRemote() }
func (b *Bck) IsRemoteAIS() bool            { return (*cmn.Bck)(b).IsRemoteAIS() }
func (b *Bck) IsQuery() bool                { return (*cmn.Bck)(b).IsQuery() }
func (b *Bck) RemoteBck() *cmn.Bck          { return (*cmn.Bck)(b).RemoteBck() }
func (b *Bck) Validate() error              { return (*cmn.Bck)(b).Validate() }
func (b *Bck) MakeUname(name string) []byte { return (*cmn.Bck)(b).MakeUname(name) }
func (b *Bck) Cname(name string) string     { return (*cmn.Bck)(b).Cname(name) }
func (b *Bck) IsEmpty() bool                { return (*cmn.Bck)(b).IsEmpty() }
func (b *Bck) HasVersioningMD() bool        { return (*cmn.Bck)(b).HasVersioningMD() }

func (b *Bck) IsRemoteS3() bool {
	if b.Provider == apc.AWS {
		return true
	}
	backend := b.Backend()
	return backend != nil && backend.Provider == apc.AWS
}

func (b *Bck) NewQuery() url.Values               { return (*cmn.Bck)(b).NewQuery() }
func (b *Bck) AddToQuery(q url.Values) url.Values { return (*cmn.Bck)(b).AddToQuery(q) }

func (b *Bck) Backend() *Bck { backend := (*cmn.Bck)(b).Backend(); return (*Bck)(backend) }

func (b *Bck) AddUnameToQuery(q url.Values, uparam string) url.Values {
	bck := (*cmn.Bck)(b)
	return bck.AddUnameToQuery(q, uparam)
}

func (b *Bck) String() string {
	var (
		s   string
		bid uint64
		bck = (*cmn.Bck)(b)
	)
	if bck.Props != nil {
		bid = b.Props.BID
	}
	if bid == 0 {
		return bck.String()
	}
	if backend := bck.Backend(); backend != nil {
		s = ", backend=" + backend.String()
	}
	return fmt.Sprintf("%s(%#x%s)", bck, BID(bid).serial(), s)
}

func (b *Bck) Equal(other *Bck, sameID, sameBackend bool) bool {
	left, right := (*cmn.Bck)(b), (*cmn.Bck)(other)
	if left.IsEmpty() || right.IsEmpty() {
		return false
	}
	if !left.Equal(right) {
		return false
	}
	if sameID && b.Props != nil && other.Props != nil && b.Props.BID != other.Props.BID {
		return false
	}
	if !sameBackend {
		return true
	}
	if backleft, backright := left.Backend(), right.Backend(); backleft != nil && backright != nil {
		return backleft.Equal(backright)
	}
	return true
}

// when the bucket is not present in the BMD:
// - always returns the corresponding *DoesNotExist error
// - Cloud bucket: fills in the props with defaults from config
// - AIS bucket: sets the props to nil
// - Remote (Cloud or Remote AIS) bucket: caller can type-cast err.(*cmn.ErrRemoteBckNotFound) and proceed
func (b *Bck) Init(bowner Bowner) (err error) {
	if err = b.Validate(); err != nil {
		return
	}
	bmd := bowner.Get()
	if err = b.init(bmd); err != nil {
		return
	}
	if backend := b.Backend(); backend != nil {
		if backend.Props == nil {
			err = backend.init(bmd)
		} else {
			p, exists := bmd.Get(backend)
			if exists {
				exists = p.BID == backend.Props.BID
			}
			if !exists {
				err = cmn.NewErrRemoteBckNotFound(backend.Bucket())
			} else if backend.Props != p {
				backend.Props = p
			}
		}
	}
	return
}

// part of lom.init (compare with the above)
func (b *Bck) InitFast(bowner Bowner) (err error) {
	bmd := bowner.Get()
	if err = b.init(bmd); err != nil {
		return
	}
	if backend := b.Backend(); backend != nil && backend.Props == nil {
		debug.Assert(apc.IsRemoteProvider(backend.Provider))
		err = backend.init(bmd)
	}
	return
}

func (b *Bck) InitNoBackend(bowner Bowner) error { return b.init(bowner.Get()) }

func (b *Bck) init(bmd *BMD) error {
	switch {
	case b.Provider == "": // ais: is the default
		b.Provider = apc.AIS
		bmd.initBckGlobalNs(b)
	case apc.IsRemoteProvider(b.Provider):
		bmd.initBck(b)
	default:
		b.Props, _ = bmd.Get(b)
	}
	if b.Props != nil {
		return nil // ok
	}
	if b.IsAIS() {
		return cmn.NewErrBckNotFound(b.Bucket())
	}
	return cmn.NewErrRemoteBckNotFound(b.Bucket())
}

// to support s3 clients:
// find an already existing bucket by name (and nothing else)
// returns an error when name cannot be unambiguously resolved to a single bucket
func InitByNameOnly(bckName string, bowner Bowner) (bck *Bck, err error, ecode int) {
	bmd := bowner.Get()
	all := bmd.getAllByName(bckName)
	switch {
	case all == nil:
		err = cmn.NewErrBckNotFound(&cmn.Bck{Name: bckName})
		ecode = http.StatusNotFound
	case len(all) == 1:
		bck = &all[0]
		if bck.Props == nil {
			err = cmn.NewErrBckNotFound(bck.Bucket())
			ecode = http.StatusNotFound
		} else if backend := bck.Backend(); backend != nil && backend.Props == nil {
			debug.Assert(apc.IsRemoteProvider(backend.Provider))
			err = backend.init(bmd)
		}
	default:
		err = fmt.Errorf("cannot unambiguously resolve bucket name %q to a single bucket (%v)", bckName, all)
		ecode = http.StatusUnprocessableEntity
	}
	return bck, err, ecode
}

func (b *Bck) CksumConf() (conf *cmn.CksumConf) { return &b.Props.Cksum }

func (b *Bck) VersionConf() cmn.VersionConf {
	if backend := b.Backend(); backend != nil && backend.Props != nil {
		conf := backend.Props.Versioning
		conf.ValidateWarmGet = b.Props.Versioning.ValidateWarmGet
		return conf
	}
	return b.Props.Versioning
}

//
// access perms
//

func (b *Bck) Allow(bit apc.AccessAttrs) error { return b.checkAccess(bit) }

func (b *Bck) checkAccess(bit apc.AccessAttrs) (err error) {
	if b.Props.Access.Has(bit) {
		return
	}
	op := apc.AccessOp(bit)
	err = cmn.NewBucketAccessDenied(b.String(), op, b.Props.Access)
	return
}

func (b *Bck) MaxPageSize() int64 {
	switch b.Provider {
	case apc.AIS:
		return apc.MaxPageSizeAIS
	case apc.AWS:
		// ref:
		// - https://docs.aws.amazon.com/cli/latest/userguide/cli-usage-pagination.html#cli-usage-pagination-serverside
		// - https://docs.openstack.org/swift/latest/api/pagination.html
		if b == nil || b.Props == nil || b.Props.Extra.AWS.MaxPageSize == 0 {
			return apc.MaxPageSizeAWS
		}
		return b.Props.Extra.AWS.MaxPageSize
	case apc.GCP:
		// ref: https://cloud.google.com/storage/docs/json_api/v1/objects/list#parameters
		return apc.MaxPageSizeGCP
	case apc.Azure:
		// ref: https://docs.microsoft.com/en-us/connectors/azureblob/#general-limits
		return apc.MaxPageSizeAzure
	default:
		return 1000
	}
}
