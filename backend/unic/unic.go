package unic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/unic/common"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/dis_operations"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/walk"
)

var entrytable_path string
var rclone_path string

// Register with Fs
func init() {
	homeDir, _ := os.UserHomeDir()
	entrytable_path = filepath.Join(homeDir, ".config", "rclone", "entrytable.jsonl")
	rclone_path = filepath.Join(homeDir, ".config", "rclone", "rclone.conf")
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

type Fs struct {
	name     string         // name of this remote
	features *fs.Features   // optional features
	opt      common.Options // parsed options
	root     string         // the path we are working on
	//upstreams []*upstream.Fs // ToDo: unic spec에 맞게 새로 정의해야함
	hashSet hash.Set // intersection of hash types
}

// Will definitely have info but maybe not meta
type Object struct {
	fs      *Fs       // what this object is part of
	id      string    // ID of the object
	remote  string    // The remote path
	size    int64     // size of the object
	modTime time.Time // modification time of the object
}

// Directory describes a OneDrive directory
type Directory struct {
	fs     *Fs    // what this object is part of
	id     string // dir ID
	remote string // The remote path
	size   int64  // size of directory and contents or -1 if unknown
	items  int64  // number of objects or -1 for unknown
}

type NodeType string

const (
	NodeTypeFile NodeType = "file"
	NodeTypeDir  NodeType = "dir"
)

type NodeEntry struct {
	Id   string   `json:"id"`
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

// entrytable을 읽는 코드가 이거 말고도 있음
// 나중에 entrytable 찾는 코드를 method로 만들어서 재사용성을 높이는 방안 생각
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

func isDirectChild(prefix, path_ string) bool {
	if prefix == "/" {
		return path.Dir(path_) == "/"
	}
	return path.Dir(path_) == prefix
}

func openEntryTable(path string) (*io.PipeReader, error) {
	pr, pw := io.Pipe()

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	go func() {
		defer f.Close()
		defer pw.Close()

		_, err := io.Copy(pw, f)
		if err != nil {
			_ = pw.CloseWithError(err)
		}
	}()

	return pr, nil
}

func (f *Fs) getList(ctx context.Context, dir string, checkDir func(path, prefix string) bool) ([]fs.DirEntry, error) {
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return nil, err
	}
	defer entryTable.Close()

	decoder := json.NewDecoder(entryTable)
	var result []fs.DirEntry

	for {
		var node NodeEntry
		var entry fs.DirEntry

		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			fmt.Println("JSON parse error:", err)
			return nil, err
		}

		if !checkDir(node.Path, dir) {
			continue
		}

		switch node.Type {
		case "file":
			o, err := f.newObject(ctx, makeRemote(node.Path, f.Root()), &node)
			if err != nil {
				return nil, err
			}
			entry = o
		case "dir":
			d, err := f.newDir(node)
			if err != nil {
				return nil, err
			}
			entry = d
		default:
			return nil, fmt.Errorf("invalid node type: %s", node.Type)
		}

		result = append(result, entry)
	}

	return result, nil
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

	list := walk.NewListRHelper(callback)

	entries, err := f.getList(ctx, dir, isUnderDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		list.Add(entry)
	}

	return list.Flush()
}

/* Fs */
// Fs
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	entries, err := f.getList(ctx, dir, isDirectChild)
	if err != nil {
		return nil, err
	}

	return fs.DirEntries(entries), nil
}

// rclone에 등록된 remote중 unic에 등록된 remote의 배열을 가져오는 method
// seen을 굳이 map[string]struct{}로 해야하나? string 배열을 사용하면 안되나?
func (f *Fs) getUpstreamRemotes() []config.Remote {
	remotes := config.GetRemotes()

	seen := make(map[string]struct{})
	for _, upstream := range f.opt.Upstreams {
		_, configName, _, _, _ := fs.ParseRemote(upstream)
		//name := strings.TrimSuffix(fsName, ":")
		seen[configName] = struct{}{}
	}

	var result []config.Remote
	for _, remote := range remotes {
		if _, ok := seen[remote.Name]; ok {
			result = append(result, remote)
		}
	}

	return result
}

func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	fmt.Println("----------Put method Start--------------")

	// Create a temporary directory
	fmt.Println("----------Create a temporary directory start--------------")
	tempDir, err := os.MkdirTemp("", "unic_upload")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir) // Clean up
	fmt.Println("----------Create a temporary directory end--------------")

	// Create the file with the correct name
	fmt.Println("----------Create a temporary file start--------------")
	tempFilePath := filepath.Join(tempDir, filepath.Base(src.Remote()))
	fmt.Printf("src.Remote(): %s\n", src.Remote())
	fmt.Printf("tempFilePath: %s\n", tempFilePath)
	tempFile, err := os.Create(tempFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	fs.Debugf(f, "----------Create a temporary file end--------------")

	// Copy content
	fs.Debugf(f, "----------Copy content to temp file start--------------")
	_, err = io.Copy(tempFile, in)

	tempFile.Sync()
	closeErr := tempFile.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", closeErr)
	}
	fs.Debugf(f, "----------Copy content to temp file end--------------")

	// Call Dis_Upload
	// args[0] is the file path. reSignal is false. LoadBalancer is RoundRobin (default).
	fs.Debugf(f, "----------dis_upload start--------------")
	fs.Debugf(f, "tempFilePath: %s, remotes: %s", tempFilePath, f.getUpstreamRemotes())

	remotePath := src.Remote()
	backendRemoteHash := sha256.Sum256([]byte(remotePath))
	fileID := hex.EncodeToString(backendRemoteHash[:])
	err = dis_operations.Dis_Upload([]string{tempFilePath, remotePath, fileID}, dis_operations.UploadTargets{Remotes: f.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	if err != nil {
		return nil, fmt.Errorf("Dis_Upload failed: %w", err)
	}
	fs.Debugf(f, "----------dis_upload end--------------")

	// Return the object
	// We construct the Object based on the source info as entry table lookup might fail or be delayed.
	return &Object{
		fs:      f,
		remote:  src.Remote(),
		size:    src.Size(),
		modTime: src.ModTime(ctx),
	}, nil
}

func extractValue(line string) string {
	parts := strings.Split(line, "=")
	if len(parts) < 2 {
		return ""
	}
	val := strings.TrimSpace(parts[1]) // {"value"}
	val = strings.TrimPrefix(val, "{\"")
	val = strings.TrimSuffix(val, "\"}")
	return val
}

func (f *Fs) Mkdir(ctx context.Context, dir string) error { return nil }
func (f *Fs) Rmdir(ctx context.Context, dir string) error { return nil }

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

// UNIC의 파일로 read와 같은 system call이 들어올 때 실제 Cloud Storage의 파일로부터 데이터를 읽어올 수 있는 통로를 제공.
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	//downloadDir := "/원하는/기본/경로/download"
	home, _ := os.UserHomeDir()
	downloadDir := filepath.Join(home, "Download")

	err := os.MkdirAll(downloadDir, 0755)
	if err != nil {
		return nil, err
	}

	// 2. Dis_Download 로직 호출
	// fileId은 파일명만 추출해서 사용된다고 가정 (filepath.Base)
	// unic의 remote 경로 전체가 필요한지, 파일명만 필요한지는 unic의 설계에 따름
	// 현재 Dis_Download는 파일명을 키로 사용함.
	fileId := o.id
	remotePath := o.remote
	// Debug
	fmt.Printf("UNIC Open 호출됨: targetName=%s, tempDir=%s\n", fileId, downloadDir)

	err = dis_operations.Dis_Download([]string{fileId, downloadDir, remotePath}, false)
	if err != nil {
		fs.Errorf(o, "Dis_Download 실패: targetName=%s, error=%v", fileId, err)
		return nil, err
	}

	// 3. 실제 생성된 파일 경로 찾기
	// Dis_Download가 tempDir 안에 readme.txt (혹은 .fcef 확장자 등)로 저장할 것이므로
	// 실제 파일의 위치를 특정해야 합니다.
	targetRealName := filepath.Base(remotePath)
	downloadedFilePath := filepath.Join(downloadDir, targetRealName)
	f, err := os.Open(downloadedFilePath)
	if err != nil {
		os.RemoveAll(downloadDir)
		return nil, err
	}
	fs.Debugf(o, "dis_download 및 os.Open(downloadedFilePath) 성공: tempDir: %s", downloadedFilePath)

	// 4. Close 시 임시 파일 삭제
	return f, nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	// dis_rm 수행하여 기존의 파일 삭제
	fs.Debugf(o, "----------Update method start----------")

	// dis_rm 수행
	fs.Debugf(o, "----------dis_operations.Dis_rm start----------")
	err := dis_operations.Dis_rm([]string{o.remote}, false)
	if err != nil {
		return err
	}
	fs.Debugf(o, "----------dis_operations.Dis_rm end----------")

	// dis_upload 수행하여 새로운 파일 업로드
	// Create a temporary directory
	fs.Debugf(o, "----------Create a temporary directory start--------------")
	tempDir, err := os.MkdirTemp("", "unic_upload")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir) // Clean up
	fs.Debugf(o, "----------Create a temporary directory end--------------")

	// Create the file with the correct name
	fs.Debugf(o, "----------Create a temporary file start--------------")
	tempFilePath := filepath.Join(tempDir, filepath.Base(src.Remote()))
	fs.Debugf(o, "src.Remote(): %s", src.Remote())
	fs.Debugf(o, "tempFilePath: %s", tempFilePath)
	tempFile, err := os.Create(tempFilePath)
	if err != nil {
		return err
	}
	fs.Debugf(o, "----------Create a temporary file end--------------")

	// Copy content
	fs.Debugf(o, "----------Copy content to temp file start--------------")
	_, err = io.Copy(tempFile, in)

	tempFile.Sync()
	closeErr := tempFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fs.Debugf(o, "----------Copy content to temp file end--------------")

	// Call Dis_Upload
	// args[0] is the file path. reSignal is false. LoadBalancer is RoundRobin (default).
	fs.Debugf(o, "----------dis_upload start--------------")
	fs.Debugf(o, "tempFilePath: %s, remotes: %s", tempFilePath, o.fs.getUpstreamRemotes())
	fileId := o.id
	remotePath := o.remote
	err = dis_operations.Dis_Upload([]string{tempFilePath, fileId, remotePath}, dis_operations.UploadTargets{Remotes: o.fs.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	if err != nil {
		return err
	}
	fs.Debugf(o, "----------dis_upload end--------------")
	fs.Debugf(o, "----------Update method end--------------")

	return nil
}

func (o *Object) Remove(ctx context.Context) error {
	fs.Debugf(o, "----------Remove method start----------")

	// dis_rm 수행
	fs.Debugf(o, "----------dis_operations.Dis_rm start----------")
	fileId := o.id
	err := dis_operations.Dis_rm([]string{fileId}, false)
	if err != nil {
		return err
	}
	fs.Debugf(o, "----------dis_operations.Dis_rm end----------")

	return nil
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
	return fmt.Sprintf("%s", d.id)
}
