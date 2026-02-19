package vfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/rclone/rclone/backend/unic"
	"github.com/rclone/rclone/fs"
)

// UnicFileHandle is an open for write handle on a File
type UnicFileHandle struct {
	baseHandle
	mu          sync.Mutex
	cond        sync.Cond // cond lock for out of sequence writes
	remote      string
	o           fs.Object
	localFile   *os.File
	localPath   string
	file        *File
	offset      int64
	flags       int
	closed      bool // set if handle has been closed
	writeCalled bool // set the first time Write() is called
	opened      bool
	truncated   bool
	isDirty     bool
}

// Check interfaces
var (
	_ io.Writer   = (*UnicFileHandle)(nil)
	_ io.WriterAt = (*UnicFileHandle)(nil)
	_ io.Closer   = (*UnicFileHandle)(nil)
)

func newUnicFileHandle(d *Dir, f *File, flags int) (*UnicFileHandle, error) {
	fmt.Println("unichandle: newUnicFileHandle: start")
	lPath, _ := f.Fs().(*unic.Fs).MakeOSDownloadPath(f.Path())

	fh := &UnicFileHandle{
		remote:    f.Path(),
		flags:     flags,
		file:      f,
		localPath: lPath,
		isDirty:   !f.exists(),
	}

	fh.cond = sync.Cond{L: &fh.mu}
	fh.file.addWriter(fh)
	return fh, nil
}

// removeUnic handles file deletion for unic backend
func (f *File) removeUnic() error {
	fmt.Println("unichandle: removeUnic: start")

	// local cache 삭제
	if fsObj, ok := f.Fs().(*unic.Fs); ok {
		localPath, _ := fsObj.MakeOSDownloadPath(f.Path())
		fmt.Printf("unichandle: removeUnic: localPath: %s\n", localPath)
		err := os.Remove(localPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.o != nil {
		return f.o.Remove(context.TODO())
	}
	return nil
}

func (f *File) openUnic(flags int) (fh *UnicFileHandle, err error) {
	fmt.Println("unichandle: openUnic: start")
	f.mu.RLock()
	d := f.d
	f.mu.RUnlock()
	// FIXME chunked
	if flags&accessModeMask != os.O_RDONLY && d.vfs.Opt.ReadOnly {
		return nil, EROFS
	}
	// fs.Debugf(f.Path(), "File.openRW")

	fh, err = newUnicFileHandle(d, f, flags)
	if err != nil {
		fs.Debugf(f.Path(), "File.openRW failed: %v", err)
		return nil, err
	}
	return fh, nil
}

// returns whether it is OK to truncate the file
func (fh *UnicFileHandle) safeToTruncate() bool {
	return fh.truncated || fh.flags&os.O_TRUNC != 0 || !fh.file.exists()
}

// openPending opens the file if there is a pending open
//
// call with the lock held
func (fh *UnicFileHandle) openPending() (err error) {
	fmt.Println("unichandle: openPending: start")
	if fh.opened {
		return nil
	}
	fh.o = fh.file.getObject()
	// 1. 기존 파일 다운로드 트리거
	// 단, 파일을 새로 쓰거나(O_TRUNC), 새로 만드는(O_CREATE) 모드이면서
	// 기존 내용을 무시해도 되는 상황이라면 다운로드를 건너뛸 수 있습니다.
	// 여기서는 안전하게 'Truncate' 플래그가 없을 때만 다운로드하도록 구성해볼 수 있습니다.
	//fmt.Print("check: %d, %d \n", fh.o != nil, (fh.flags&os.O_TRUNC == 0))
	if fh.o != nil && (fh.flags&os.O_TRUNC == 0) {
		fmt.Print("check if disdownload\n")
		rc, err := fh.o.Open(context.TODO())
		if err != nil {
			return fmt.Errorf("unic: openPending download failed: %w", err)
		}
		_ = rc.Close()
	}

	fmt.Print("check if disdownload completed\n")
	// 2. 실제 사용할 로컬 파일 오픈
	// 사용자가 요청한 flags(RDONLY, WRONLY, RDWR, APPEND, TRUNC 등)가 그대로 적용됩니다.
	fh.localFile, err = os.OpenFile(fh.localPath, fh.flags, 0644)
	fmt.Print("check file local path:")
	fmt.Println(fh.localPath)
	if err != nil {
		return fmt.Errorf("unic: failed to final open with flags(%d): %w", fh.flags, err)
	}

	fh.opened = true
	return nil
}

// String converts it to printable
func (fh *UnicFileHandle) String() string {
	fmt.Println("unichandle: String: start")
	if fh == nil {
		return "<nil *UnicWriteFileHandle>"
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.file == nil {
		return "<nil *UnicWriteFileHandle.file>"
	}
	return fh.file.String() + " (uw)"
}

// Node returns the Node associated with this - satisfies Noder interface
func (fh *UnicFileHandle) Node() Node {
	fmt.Println("unichandle: Node: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.file
}

// WriteAt writes len(p) bytes from p to the underlying data stream at offset
// off. It returns the number of bytes written from p (0 <= n <= len(p)) and
// any error encountered that caused the write to stop early. WriteAt must
// return a non-nil error if it returns n < len(p).
//
// If WriteAt is writing to a destination with a seek offset, WriteAt should
// not affect nor be affected by the underlying seek offset.
//
// Clients of WriteAt can execute parallel WriteAt calls on the same
// destination if the ranges do not overlap.
//
// Implementations must not retain p.
func (fh *UnicFileHandle) WriteAt(p []byte, off int64) (n int, err error) {
	fmt.Println("unichandle: WriteAt: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.writeAt(p, off)
}

// Implementation of WriteAt - call with lock held
func (fh *UnicFileHandle) writeAt(p []byte, off int64) (n int, err error) {
	fmt.Println("unichandle: writeAt: start")
	// Deadlock 발생 지점
	//fh.mu.Lock()
	//defer fh.mu.Unlock()

	if fh.closed {
		return 0, ECLOSED
	}

	if err = fh.openPending(); err != nil {
		return 0, err
	}

	n, err = fh.localFile.WriteAt(p, off)

	if n > 0 {
		fh.isDirty = true

		newOffset := off + int64(n)
		if newOffset > fh.file.Size() {
			fh.file.setSize(newOffset)
		}

		fh.offset = newOffset
		//fh.cond.Broadcast()
	}

	if err != nil {
		fs.Errorf(fh.remote, "UnicWriteFileHandle.WriteAt error: %v", err)
		return n, err
	}

	return n, nil
}

// Write writes len(p) bytes from p to the underlying data stream. It returns
// the number of bytes written from p (0 <= n <= len(p)) and any error
// encountered that caused the write to stop early. Write must return a non-nil
// error if it returns n < len(p). Write must not modify the slice data, even
// temporarily.
//
// Implementations must not retain p.
func (fh *UnicFileHandle) Write(p []byte) (n int, err error) {
	fmt.Println("unichandle: Write: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	// Since we can't seek, just call WriteAt with the current offset
	return fh.writeAt(p, fh.offset)
}

// WriteString a string to the file
func (fh *UnicFileHandle) WriteString(s string) (n int, err error) {
	fmt.Println("unichandle: WriteString: start")
	return fh.Write([]byte(s))
}

// Offset returns the offset of the file pointer
func (fh *UnicFileHandle) Offset() (offset int64) {
	fmt.Println("unichandle: Offset: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.offset
}

// close the file handle returning EBADF if it has been
// closed already.
//
// Must be called with fh.mu held
func (fh *UnicFileHandle) close() (err error) {
	fmt.Println("unichandle: close: start")
	if fh.closed {
		return ECLOSED
	}
	fh.closed = true

	// leave writer open until file is transferred
	defer fh.file.delWriter(fh)

	// If file not opened and not safe to truncate then leave file intact
	if !fh.opened {
		return nil
	}

	if fh.localFile != nil {
		_ = fh.localFile.Sync() // 디스크 동기화 강제 실행
	}

	if fh.isDirty {
		fs.Debugf(fh.remote, "Fixes on file Detected")
		var newObj fs.Object
		var uploadErr error
		if fh.flags&os.O_CREATE != 0 {
			newObj, uploadErr = fh.file.Fs().(*unic.Fs).Put_(context.TODO(), fh.localFile, fh.remote) //fh.d.vfs.Fs().Put(context.Background(), fh.localPath, fh.o, nil)
			if uploadErr != nil {
				fs.Errorf(fh.remote, "upload failed: %v", uploadErr)
				return uploadErr
			}
		} else {
			//update
		}

		// 5. 성공 시 VFS의 객체 정보 업데이트
		fh.file.setObject(newObj)
		fh.file.setSize(newObj.Size())
		fmt.Printf("unichandle: close: file size: %d\n", fh.file.Size())
		fh.isDirty = false
	}

	if closeErr := fh.localFile.Close(); closeErr != nil {
		fs.Errorf(fh.remote, "File Closing Failed: %v", closeErr)
	}

	return nil
}

// Close closes the file
func (fh *UnicFileHandle) Close() error {
	fmt.Println("unichandle: Close: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.close()
}

// Flush is called on each close() of a file descriptor. So if a
// filesystem wants to return write errors in close() and the file has
// cached dirty data, this is a good place to write back data and
// return any errors. Since many applications ignore close() errors
// this is not always useful.
//
// NOTE: The flush() method may be called more than once for each
// open(). This happens if more than one file descriptor refers to an
// opened file due to dup(), dup2() or fork() calls. It is not
// possible to determine if a flush is final, so each flush should be
// treated equally. Multiple write-flush sequences are relatively
// rare, so this shouldn't be a problem.
//
// Filesystems shouldn't assume that flush will always be called after
// some writes, or that if will be called at all.
func (fh *UnicFileHandle) Flush() error {
	fmt.Println("unichandle: Flush: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		fs.Debugf(fh.remote, "UnicFileHandle.Flush nothing to do")
		return nil
	}

	//로컬 파일 디스크 저장
	if fh.localFile != nil {
		err := fh.localFile.Sync()
		if err != nil {
			fs.Errorf(fh.remote, "Flush failed: %v", err)
			return err
		}
	}
	return nil
}

// Release is called when we are finished with the file handle
//
// It isn't called directly from userspace so the error is ignored by
// the kernel
func (fh *UnicFileHandle) Release() error {
	fmt.Println("unichandle: Release: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		fs.Debugf(fh.remote, "UnicFileHandle.Release nothing to do")
		return nil
	}
	fs.Debugf(fh.remote, "UnicFileHandle.Release closing")
	err := fh.close()
	if err != nil {
		fs.Errorf(fh.remote, "UnicFileHandle.Release error: %v", err)
		//} else {
		// fs.Debugf(fh.remote, "WriteFileHandle.Release OK")
	}
	return err
}

// Stat returns info about the file
func (fh *UnicFileHandle) Stat() (os.FileInfo, error) {
	fmt.Println("unichandle: Stat: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.file, nil
}

// Truncate file to given size
func (fh *UnicFileHandle) Truncate(size int64) (err error) {
	fmt.Println("unichandle: Truncate: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if err = fh.openPending(); err != nil {
		return err
	}

	fmt.Printf("unichandle: Truncate: size: %d\n", size)

	// 1. 로컬 파일 포인터가 있는지 확인
	if fh.localFile == nil {
		return ECLOSED
	}

	// 2. 실제 로컬 파일의 크기를 변경합니다. (가장 중요!)
	err = fh.localFile.Truncate(size)
	if err != nil {
		fs.Errorf(fh.remote, "Truncate 실패: %v", err)
		return err
	}

	// 3. 수정되었으므로 dirty 플래그 설정
	fh.isDirty = true

	// 4. 만약 현재 오프셋이 잘려나간 사이즈보다 크다면, 오프셋을 조정해줍니다.
	if fh.offset > size {
		fh.offset = size
	}

	return nil
}

func (fh *UnicFileHandle) Seek(offset int64, whence int) (int64, error) {
	fmt.Println("unichandle: Seek: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if fh.closed {
		return 0, ECLOSED
	}

	// 1. 아직 파일이 열리지 않았다면 (Lazy Load)
	if !fh.opened || fh.localFile == nil {
		// 읽기/쓰기 동작이 아니더라도 Seek이 들어오면 파일을 준비시키는 것이 가장 안전합니다.
		// 예를 들어 파일 끝으로 이동해서 크기를 알아내려는 동작 등이 있을 수 있기 때문입니다.
		if err := fh.openPending(); err != nil {
			return 0, err
		}
	}

	// 2. 실제 로컬 파일의 포인터를 이동시킵니다.
	// whence: 0(시작점 기준), 1(현재 위치 기준), 2(파일 끝 기준)
	newOffset, err := fh.localFile.Seek(offset, whence)
	if err != nil {
		return 0, err
	}

	// 3. 핸들러의 오프셋을 최신화합니다.
	fh.offset = newOffset
	return newOffset, nil
}

// Read reads up to len(p) bytes into p.
func (fh *UnicFileHandle) Read(p []byte) (n int, err error) {
	fmt.Println("unichandle: Read: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	n, err = fh.readAt(p, fh.offset)
	fh.offset += int64(n)
	return n, err
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes read (0 <=
// n <= len(p)) and any error encountered.
func (fh *UnicFileHandle) ReadAt(p []byte, off int64) (n int, err error) {
	fmt.Println("unichandle: ReadAt: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.readAt(p, off)
}

func (fh *UnicFileHandle) readAt(p []byte, off int64) (n int, err error) {
	fmt.Println("unichandle: readAt: start")
	if err := fh.openPending(); err != nil {
		return 0, err
	}

	if fh.localFile == nil {
		return 0, ECLOSED
	}

	fmt.Println("Check readAt")
	return fh.localFile.ReadAt(p, off)
}

func (fh *UnicFileHandle) readOnly() bool {
	fmt.Println("unichandle: readOnly: start")
	return (fh.flags & accessModeMask) == os.O_RDONLY
}

// Sync commits the current contents of the file to stable storage. Typically,
// this means flushing the file system's in-memory copy of recently written
// data to disk.
func (fh *UnicFileHandle) Sync() error {
	fmt.Println("unichandle: Sync: start")
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		return ECLOSED
	}
	if !fh.opened {
		return nil
	}
	if fh.readOnly() {
		return nil
	}
	return fh.localFile.Sync()
}

// Name returns the name of the file from the underlying Object.
func (fh *UnicFileHandle) Name() string {
	fmt.Println("unichandle: Name: start")
	return fh.file.String()
}
