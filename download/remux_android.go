//go:build android && cgo

package download

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <pthread.h>
#include <stdlib.h>
#include <stdatomic.h>
#include <stdio.h>
typedef int (*remux_fn)(const char*,const char*,const char*,const char*,const char*,const atomic_int*,char*,size_t);
static pthread_once_t once = PTHREAD_ONCE_INIT;
static remux_fn remux;
static void load_remux(void) {
 void* lib = dlopen("libzeroremux.so", RTLD_NOW|RTLD_LOCAL);
 if(lib) remux=(remux_fn)dlsym(lib,"zero_remux");
}
static atomic_int* new_cancel(void) {atomic_int* flag=malloc(sizeof(atomic_int));if(flag)atomic_init(flag,0);return flag;}
static void cancel_remux(atomic_int* flag) {atomic_store(flag,1);}
static int run_remux(const char* in,const char* out,const char* title,const char* artist,const char* album,const atomic_int* flag,char* error,size_t size) {
 pthread_once(&once,load_remux);
 if(!remux){snprintf(error,size,"Android 音频封装组件不可用");return -1;}
 return remux(in,out,title,artist,album,flag,error,size);
}
*/
import "C"
import (
	"context"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/source"
	"strings"
	"unsafe"
)

func platformRemux(ctx context.Context, in, out string, track source.Track) error {
	args := []*C.char{C.CString(in), C.CString(out), C.CString(strings.TrimSpace(track.Title)), C.CString(strings.TrimSpace(track.Artist)), C.CString(strings.TrimSpace(track.Album))}
	defer func() {
		for _, p := range args {
			C.free(unsafe.Pointer(p))
		}
	}()
	flag := C.new_cancel()
	if flag == nil {
		return fmt.Errorf("无法分配封装任务")
	}
	defer C.free(unsafe.Pointer(flag))
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cancel_remux(flag); close(done) })
	var message [512]C.char
	code := C.run_remux(args[0], args[1], args[2], args[3], args[4], flag, &message[0], 512)
	if !stop() {
		<-done
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s", C.GoString(&message[0]))
	}
	return nil
}
