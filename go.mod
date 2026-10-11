module simmusic

// WASAPI session ducking opt-out and MMCSS render-thread scheduling.
replace github.com/ebitengine/oto/v3 => ./third_party/oto

go 1.27.1

require (
	fyne.io/systray v1.12.2
	github.com/bogem/id3v2/v2 v2.1.4
	github.com/dhowden/tag v0.0.0-20240417053706-3d75831295e8
	github.com/dop251/goja v0.0.0-20261006212518-44620e89763c
	github.com/gopxl/beep/v2 v2.1.1
	github.com/tc-hib/winres v0.3.1
	golang.org/x/image v0.46.0
	golang.org/x/sys v0.48.0
)

require (
	github.com/dlclark/regexp2/v2 v2.8.1 // indirect
	github.com/ebitengine/oto/v3 v3.3.2 // indirect
	github.com/ebitengine/purego v0.8.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/jfreymuth/oggvorbis v1.0.5 // indirect
	github.com/jfreymuth/vorbis v1.0.2 // indirect
	github.com/mewkiz/flac v1.0.12 // indirect
	github.com/mewkiz/pkg v0.0.0-20230226050401-4010bf0fec14 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/text v0.42.0 // indirect
)
