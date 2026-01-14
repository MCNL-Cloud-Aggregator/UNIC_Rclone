package unic

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/unic/common"
	"github.com/rclone/rclone/backend/unic/upstream"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "unic",
		Description: "Unified Namespace of Integrated Cloudstorage",
		NewFs:       NewFs,
		MetadataInfo: &fs.MetadataInfo{
			Help: `Any metadata supported by the underlying remote is read and written.`,
		},
		Options: []fs.Option{{
			Name:     "upstreams",
			Help:     "List of space separated upstreams.\n\nCan be 'upstreama:test/dir upstreamb:', '\"upstreama:test/space:ro dir\" upstreamb:', etc.",
			Required: true,
		}, {
			Name:    "cache_time",
			Help:    "Cache time of usage and free space (in seconds).\n\nThis option is only useful when a path preserving policy is used.",
			Default: 120,
		}},
	})
}

//Fs에 들어갈 property 정의
//NewFs 정의
//common object 정의
//upstream Fs 정의
//로직 구성

// upstream fs, backend fs
// NewFs

type Fs struct {
	name      string         // name of this remote
	features  *fs.Features   // optional features -> 이게 정확하게 뭔지 모르겠음
	opt       common.Options // parsed options -> 이게 정확하게 뭔지 모르겠음2
	root      string         // the path we are working on
	upstreams []*upstream.Fs // ToDo: unic spec에 맞게 새로 정의해야함
	hashSet   hash.Set       // intersection of hash types
}

type Object struct {
}

func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	// Parse config into Options struct
	opt := new(common.Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	// Trim root
	root = strings.Trim(root, "/")

	// Make upstreams from opt.Upstreams
	upstreams := make([]*upstream.Fs, len(opt.Upstreams))
	errs := Errors(make([]error, len(opt.Upstreams)))
	multithread(len(opt.Upstreams), func(i int) {
		u := opt.Upstreams[i]
		upstreams[i], errs[i] = upstream.New(ctx, u, root, opt)
	})

	// Error handling while making upstreams
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// Make Fs object
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		features: &fs.Features{},
	}

	var features = (&fs.Features{
		CaseInsensitive:          false, // has case insensitive files
		DuplicateFiles:           true,  // allows duplicate files
		ReadMimeType:             false, // can read the mime type of objects
		WriteMimeType:            false, // can set the mime type of objects
		CanHaveEmptyDirectories:  true,  // can have empty directories
		BucketBased:              ?, // is bucket based (like s3, swift, etc.)
		BucketBasedRootOK:        ?, // is bucket based and can use from root
		SetTier:                  false,  // allows set tier functionality on objects
		GetTier:                  false,  // allows to retrieve storage tier of objects
		ServerSideAcrossConfigs:  false,  // can server-side copy between different remotes of the same type
		IsLocal:                  false,  // is the local backend
		SlowModTime:              true,  // if calling ModTime() generally takes an extra transaction
		SlowHash:                 true,  // if calling Hash() generally takes an extra transaction
		ReadMetadata:             bool,  // can read metadata from objects
		WriteMetadata:            bool,  // can write metadata to objects
		UserMetadata:             bool,  // can read/write general purpose metadata
		ReadDirMetadata:          bool,  // can read metadata from directories (implements Directory.Metadata)
		WriteDirMetadata:         bool,  // can write metadata to directories (implements Directory.SetMetadata)
		WriteDirSetModTime:       bool,  // can write metadata to directories (implements Directory.SetModTime)
		UserDirMetadata:          bool,  // can read/write general purpose metadata to/from directories
		DirModTimeUpdatesOnWrite: bool,  // indicate writing files to a directory updates its modtime
		FilterAware:              bool,  // can make use of filters if provided for listing
		PartialUploads:           bool,  // uploaded file can appear incomplete on the fs while it's being uploaded
		NoMultiThreading:         bool,  // set if can't have multiplethreads on one download open
		Overlay:                  bool,  // this wraps one or more backends to add functionality
		ChunkWriterDoesntSeek:    bool,  // set if the chunk writer doesn't need to read the data more than once
	}).Fill(ctx, f)

	return f, nil
}

/* Fs */
// Fs
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return nil, nil
}

func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return &Object{}, nil
}

func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return &Object{}, nil
}

func (f *Fs) Mkdir(ctx context.Context, dir string) error { return nil }
func (f *Fs) Rmdir(ctx context.Context, dir string) error { return nil }

// Info
// func (f *Fs) Name() string

// func (f *Fs) Root() string

// func (f *Fs) String() string

//func (f *Fs) Features() *fs.Features

func (f *Fs) Name() string           { return f.name }
func (f *Fs) Root() string           { return f.root }
func (f *Fs) String() string         { return f.name }
func (f *Fs) Features() *fs.Features { return f.features }

func (f *Fs) Precision() time.Duration {
	return time.Second // 최소 1초 정밀도
}

func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None) // 최소 해시 없음
}

/* Object */
// DirEntry
func (o *Object) Fs() fs.Info                                            { return nil }
func (o *Object) String() string                                         { return "object" }
func (o *Object) Remote() string                                         { return "" }
func (o *Object) ModTime(ctx context.Context) time.Time                  { return time.Now() }
func (o *Object) Size() int64                                            { return 0 }
func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) { return "", nil }
func (o *Object) Storable() bool                                         { return true }
func (o *Object) SetModTime(ctx context.Context, t time.Time) error      { return nil }
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return nil
}
func (o *Object) Remove(ctx context.Context) error { return nil }

func multithread(num int, fn func(int)) {
	var wg sync.WaitGroup
	for i := 0; i < num; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			fn(i)
		}()
	}
	wg.Wait()
}
