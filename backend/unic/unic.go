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

	// features 정의
	var features = (&fs.Features{
		CaseInsensitive:          false, // has case insensitive files
		DuplicateFiles:           false, // 동일한 경로, 이름을 가진 파일이 존재할 수 있는지
		ReadMimeType:             false, // can read the mime type of objects
		WriteMimeType:            false, // can set the mime type of objects
		CanHaveEmptyDirectories:  true,  // can have empty directories
		BucketBased:              false, // cloud의 저장소 안에 bucket이라는 최상위 저장소가 존재하는지 여부
		BucketBasedRootOK:        false, // bucket이 있을 시 root에 대한 요청을 허가할 것인지 아닌지 여부
		SetTier:                  false, // 엑세스 빈도에 따라 파일의 등급을 나누는 기능이 있는지
		GetTier:                  false, // 엑세스 빈도에 따라 파일의 등급을 나누는 기능이 있는지
		ServerSideAcrossConfigs:  false, // local을 거치지 않고 server끼리의 파일 복사가 가능한지 여부
		IsLocal:                  false, // is the local backend
		SlowModTime:              true,  // modetime을 확인하는데 시간이 많이 드는지
		SlowHash:                 true,  // hash를 확인하는데 시간이 많이 드는지
		ReadMetadata:             false, // can read metadata from objects, Object.Metadata() 구현 필요, 일단은 false
		WriteMetadata:            false, // can write metadata to objects, 일단은 false
		UserMetadata:             false, // user가 정의한 메타데이터 정의 가능 여부
		ReadDirMetadata:          false, // can read metadata from directories (implements Directory.Metadata), 일단은 false
		WriteDirMetadata:         false, // can write metadata to directories (implements Directory.SetMetadata), 일단은 false
		WriteDirSetModTime:       false, // can write metadata to directories (implements Directory.SetModTime), 일단은 false
		UserDirMetadata:          false, // user가 정의한 메타데이터 정의 가능 여부
		DirModTimeUpdatesOnWrite: false, // indicate writing files to a directory updates its modtime, 일단은 false
		FilterAware:              false, // 파일 필터링 기능을 서버에서 지원해서 그 기능을 사용할 것인지
		PartialUploads:           false, // 업로드중인 파일이 서버상에서 미완성된 상태로 다른 사용자에게 보여주는지 여부
		NoMultiThreading:         true,  // set if can't have multiplethreads on one download open
		Overlay:                  true,  // this wraps one or more backends to add functionality
		ChunkWriterDoesntSeek:    true,  // 대용량 파일을 업로드할 시 rclone은 chunkwriter를 이용해서 파일을 업로드하는데 chunkwriter가 업로드 도중 끊기면 해당 부분을 seek해야할 수도 있음. 그걸 가능하게 할지말지 설정.
	}).Fill(ctx, f)

	// Fs 객체에 features 저장
	f.features = features

	// 추후 필요시 Move, Purge, ListR 등 추가

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
