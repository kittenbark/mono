package mono

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
)

var (
	RpsLimiterDefaultQuota    atomic.Int64
	DefaultTailwind           atomic.Pointer[Tailwind]
	CurrentEnv                atomic.Pointer[Environment]
	InMemoryFilesizeThreshold int64       = 1 << 20
	LazyStatics               atomic.Bool // TODO: implement it :)
	TempDir                   = ""        // default OS' temp dir
	TempDirClean              = true

	Filetypes = map[string][]string{
		"img":   {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".heic"},
		"video": {".mp4", ".mov", ".webm", ".heiv"},
		"audio": {".mp3", ".wav", ".flac", ".ogg", ".aac"},
	}
	FiletypesTags = map[string]string{
		"img":   `<img src="%s" alt="%s">`,
		"video": `<video src="%s" alt="%s" preload="auto" loop autoplay muted controls>Does you browser support videos?</video>`,
		"audio": `<audio src="%s" alt="%s" onloadedmetadata="this.volume=0.25" controls>Does your Linux support audio?</audio>`,
	}
)

type Environment int64

const (
	EnvUnspecified Environment = iota
	EnvDev
	EnvProd
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

	if IsDev() {
		LazyStatics.Store(true)
	}

	DefaultTailwind.Store(&Tailwind{
		CLI: "tailwind",
	})
	if monoTailwind, ok := os.LookupEnv("MONO_TAILWIND"); ok {
		DefaultTailwind.Load().CLI = monoTailwind
	}
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
