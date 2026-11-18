//go:build android && !cgo

package download

import (
	"context"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/source"
)

func platformRemux(context.Context, string, string, source.Track) error {
	return fmt.Errorf("Android 音频封装需要启用原生组件")
}
