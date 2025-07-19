package mono

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
)

var (
	RpsLimiterDefaultQuota    atomic.Int64
	CurrentEnv                atomic.Pointer[Environment]
	InMemoryFilesizeThreshold int64 = 1 << 20
	TempDir                         = "" // default OS' temp dir
	TempDirClean                    = true
	EnableTLS                       = EnableTLSUnspecified
	Log                             = slog.Default()

	Filetypes = map[string][]string{
		"img":   {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".heic"},
		"video": {".mp4", ".mov", ".webm", ".heiv"},
		"audio": {".mp3", ".wav", ".flac", ".ogg", ".aac"},
	}
	FiletypesTags = map[string]string{
		"img":   `<img src="%s" alt="%s">`,
		"video": `<video src="%s" alt="%s" preload="metadata" loop autoplay muted controls>Does you browser support videos?</video>`,
		"audio": `<audio src="%s" alt="%s" onloadedmetadata="this.volume=0.25" controls>Does your Linux support audio?</audio>`,
	}
)

type Environment int64

const (
	envUnspecified Environment = iota
	EnvDev
	EnvProd
)
const (
	EnableTLSUnspecified = iota
	EnableTLSTrue        = 1
	EnableTLSFalse       = 2
)

func SetEnv(env Environment) {
	CurrentEnv.Store(&env)
}

func IsDev() bool {
	return *CurrentEnv.Load() == EnvDev
}

func init() {
	RpsLimiterDefaultQuota.Store(10)
	if monoRps, ok := os.LookupEnv("MONO_RPS"); ok {
		if rps, err := strconv.ParseInt(monoRps, 10, 64); err == nil {
			RpsLimiterDefaultQuota.Store(rps)
		}
	}

	env := EnvDev
	if os.Getenv("MONO_ENV") == "PROD" {
		env = EnvProd
	}
	CurrentEnv.Store(&env)
}

var statusMessageCache = [600][]byte{}

func responseError(rw http.ResponseWriter, status int) error {
	if len(statusMessageCache[status]) == 0 {
		statusMessageCache[status] = []byte(fmt.Sprintf("%d %s", status, http.StatusText(status)))
	}

	rw.WriteHeader(status)
	if _, err := rw.Write(statusMessageCache[status]); err != nil {
		return err
	}
	return nil
}

func enableTLS() bool {
	if EnableTLS != EnableTLSUnspecified {
		return EnableTLS == EnableTLSTrue
	}

	switch runtime.GOOS {
	case "windows", "darwin":
		return false
	default:
		return true
	}
}
