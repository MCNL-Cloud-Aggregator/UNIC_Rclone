package unic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/unic/common"
	"github.com/rclone/rclone/backend/unic/upstream"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/walk"
)

var entrytable_path = "/" //여기에는 inodetable이 저장될 경로를 쓸 것임

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

// Will definitely have info but maybe not meta
type Object struct {
	fs      *Fs       // what this object is part of
	id      int       // ID of the object
	remote  string    // The remote path
	size    int64     // size of the object
	modTime time.Time // modification time of the object
}

// Directory describes a OneDrive directory
type Directory struct {
	fs     *Fs    // what this object is part of
	id     int    // dir ID
	remote string // The remote path
	size   int64  // size of directory and contents or -1 if unknown
	items  int64  // number of objects or -1 for unknown
}

/*
// Object is a filesystem like object provided by an Fs
type Object interface {
	ObjectInfo

	// SetModTime sets the metadata on the object to set the modification date
	SetModTime(ctx context.Context, t time.Time) error

	// Open opens the file for read.  Call Close() on the returned io.ReadCloser
	Open(ctx context.Context, options ...OpenOption) (io.ReadCloser, error)

	// Update in to the object with the modTime given of the given size
	//
	// When called from outside an Fs by rclone, src.Size() will always be >= 0.
	// But for unknown-sized objects (indicated by src.Size() == -1), Upload should either
	// return an error or update the object properly (rather than e.g. calling panic).
	Update(ctx context.Context, in io.Reader, src ObjectInfo, options ...OpenOption) error

	// Removes this object
	Remove(ctx context.Context) error
}

// ObjectInfo provides read only information about an object.
type ObjectInfo interface {
	DirEntry

	// Hash returns the selected checksum of the file
	// If no checksum is available it returns ""
	Hash(ctx context.Context, ty hash.Type) (string, error)

	// Storable says whether this object can be stored
	Storable() bool
}

// DirEntry provides read only information about the common subset of
// a Dir or Object.  These are returned from directory listings - type
// assert them into the correct type.
type DirEntry interface {
	// Fs returns read only access to the Fs that this object is part of
	Fs() Info

	// String returns a description of the Object
	String() string

	// Remote returns the remote path
	Remote() string

	// ModTime returns the modification date of the file
	// It should return a best guess if one isn't available
	ModTime(context.Context) time.Time

	// Size returns the size of the file
	Size() int64
}

// Directory is a filesystem like directory provided by an Fs
type Directory interface {
	DirEntry

	// Items returns the count of items in this directory or this
	// directory and subdirectories if known, -1 for unknown
	Items() int64

	// ID returns the internal ID of this directory if known, or
	// "" otherwise
	ID() string
}
*/
// -------------------------------------------------------------------------------------------------------
type NodeType string

const (
	NodeTypeFile NodeType = "file"
	NodeTypeDir  NodeType = "dir"
)

type NodeEntry struct {
	Id   int      `json:"id"`
	Path string   `json:"path"`
	Name string   `json:"name"`
	Type NodeType `json:"type"`

	Size    int64     `json:"size"`    // size of the object
	ModTime time.Time `json:"modtime"` // modification time of the object
	Items   int64     `json:"items"`   // number of objects or -1 for unknown
}

func (t NodeType) Valid() bool {
	return t == NodeTypeFile || t == NodeTypeDir
}

func (i *NodeEntry) IsDir() bool  { return i.Type == NodeTypeDir }
func (i *NodeEntry) IsFile() bool { return i.Type == NodeTypeFile }

// -------------------------------------------------------------------------------------------------------
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	// Parse config into Options struct
	opt := new(common.Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	// Trim root
	root = strings.Trim(root, "/")

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

func (f *Fs) newObject(ctx context.Context, remote string, node *NodeEntry) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
	}
	var err error
	if node != nil {
		o.id = node.Id
		o.size = node.Size
		o.modTime = node.ModTime
	} else {
		foundNode, err := f.findNodeFromTable(remote)
		if err != nil {
			return nil, err
		}

		o.id = foundNode.Id
		o.size = foundNode.Size
		o.modTime = foundNode.ModTime
	}
	return o, err
}

func (f *Fs) NewObject(ctx context.Context, remote string) (entry fs.Object, err error) {
	return f.newObject(ctx, remote, nil)
}

func (f *Fs) findNodeFromTable(remote string) (*NodeEntry, error) {
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return nil, err
	}
	defer entryTable.Close()

	decoder := json.NewDecoder(entryTable)
	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if node.Path == remote {
			return &node, nil
		}
	}

	return nil, fs.ErrorObjectNotFound
}

func (f *Fs) newDir(node NodeEntry) (entry fs.Directory, err error) {
	d := &Directory{
		fs:     f,
		id:     node.Id,
		remote: node.Path,
		size:   -1,
		items:  -1,
	}
	return d, nil
}

func isUnderDir(path, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}

	prefix = strings.TrimSuffix(prefix, "/")

	if path == prefix {
		return false
	}

	return strings.HasPrefix(path, prefix+"/")
}

func isDirectChild(dir, remote string) bool {
	if dir == "/" {
		return path.Dir(remote) == "/"
	}
	return path.Dir(remote) == dir
}

// makeRemote converts an absolute path (node.Path) into
// a Fs.root-relative path (remote) suitable for DirEntry.
func makeRemote(path string, prefix string) string {
	// root 기준이면 그대로 상대경로
	if prefix == "" || prefix == "/" {
		return strings.TrimPrefix(path, "/")
	}

	// root 끝의 '/' 제거
	prefix = strings.TrimSuffix(prefix, "/")

	// p가 root 자체이면 remote는 빈 문자열 (dir entry용)
	if path == prefix {
		return ""
	}

	// root 하위 경로만 허용
	prefix_ := prefix + "/"
	if !strings.HasPrefix(path, prefix_) {
		// root 밖의 경로 → List/ListR 대상 아님
		return ""
	}

	// root 기준 상대 경로로 변환
	return strings.TrimPrefix(path, prefix_)
}

func (f *Fs) ListR(ctx context.Context, dir string, callback fs.ListRCallback) (err error) {
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return err
	}
	defer entryTable.Close()
	decoder := json.NewDecoder(entryTable)
	list := walk.NewListRHelper(callback)

	for {
		var node NodeEntry
		var entry fs.DirEntry

		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break // 파일 끝 도달
			}
			fmt.Println("JSON parse error:", err)
			return err
		}

		if !isUnderDir(node.Path, f.Root()) {
			continue
		}

		switch node.Type {
		case "file":
			// file 전용 로직
			o, err := f.newObject(ctx, makeRemote(node.Path, f.Root()), &node) //node에 적힌 데이터를 초기화한 object 객체를 저장
			if err != nil {
				return err
			}
			entry = o

		case "dir":
			// dir 전용 로직
			d, err := f.newDir(node) //node에 적힌 데이터를 초기화한 directory 객체를 저장
			if err != nil {
				return err
			}
			entry = d

		default:
			return fmt.Errorf("invalid node type: %s", node.Type)
		}

		list.Add(entry)
	}

	return list.Flush()
}

/* Fs */
// Fs
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return nil, nil
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
func (o *Object) Fs() fs.Info {
	return o.fs
}

func (o *Object) String() string {
	return o.remote
}

func (o *Object) Remote() string {
	return o.remote
}

func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

func (o *Object) Size() int64 {
	return o.size
}

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

func (d *Directory) Fs() fs.Info {
	return d.fs
}

func (d *Directory) String() string {
	return d.remote
}

func (d *Directory) Remote() string {
	return d.remote
}

func (d *Directory) ModTime(context.Context) time.Time {
	return time.Time{}
}

func (d *Directory) Size() int64 {
	return d.size
}

func (d *Directory) Items() int64 {
	return d.items
}

func (d *Directory) ID() string {
	return fmt.Sprintf("%d", d.id)
}
