package wcdb

import (
	"strings"
	"testing"
	"unsafe"
)

func TestOpenFailureClosesAllocatedHandle(t *testing.T) {
	oldOpen := sqlite3_open_v2
	oldClose := sqlite3_close_v2
	oldErrmsg := sqlite3_errmsg
	t.Cleanup(func() {
		sqlite3_open_v2 = oldOpen
		sqlite3_close_v2 = oldClose
		sqlite3_errmsg = oldErrmsg
	})
	const allocated = uintptr(42)
	sqlite3_open_v2 = func(_ string, handle *uintptr, _ int32, _ *byte) int32 {
		*handle = allocated
		return 14
	}
	closed := uintptr(0)
	sqlite3_close_v2 = func(handle uintptr) int32 {
		closed = handle
		return SQLITE_OK
	}
	sqlite3_errmsg = func(uintptr) unsafe.Pointer { return nil }
	if _, err := OpenPlain("unopenable.db", false); err == nil {
		t.Fatal("OpenPlain should report sqlite3_open_v2 failure")
	}
	if closed != allocated {
		t.Fatalf("closed handle = %d, want allocated handle %d", closed, allocated)
	}
}

func TestExecFreesSQLiteErrorMessage(t *testing.T) {
	oldExec := sqlite3_exec
	oldFree := sqlite3_free
	t.Cleanup(func() {
		sqlite3_exec = oldExec
		sqlite3_free = oldFree
	})
	message := append([]byte("broken statement"), 0)
	messagePtr := unsafe.Pointer(&message[0])
	sqlite3_exec = func(_ uintptr, _ string, _ uintptr, _ uintptr, errOut *unsafe.Pointer) int32 {
		*errOut = messagePtr
		return 1
	}
	freed := unsafe.Pointer(nil)
	sqlite3_free = func(p unsafe.Pointer) { freed = p }
	err := (&DB{handle: 1}).Exec("bad sql")
	if err == nil || !strings.Contains(err.Error(), "broken statement") {
		t.Fatalf("Exec error = %v", err)
	}
	if freed != messagePtr {
		t.Fatalf("sqlite3_free pointer = %p, want %p", freed, messagePtr)
	}
}

func TestBindArgsReturnsSQLiteBindFailure(t *testing.T) {
	oldBind := sqlite3_bind_int64
	t.Cleanup(func() { sqlite3_bind_int64 = oldBind })
	sqlite3_bind_int64 = func(uintptr, int32, int64) int32 { return 25 }
	err := bindArgs(1, []any{int64(7)})
	if err == nil || !strings.Contains(err.Error(), "bind arg 1 rc=25") {
		t.Fatalf("bindArgs error = %v", err)
	}
}
