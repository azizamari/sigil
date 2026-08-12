package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fixture is a synthetic source standing in for a content type. Robustness
// depends heavily on texture and motion, so a single fixture would report a
// number that means nothing for the other two.
type Fixture struct {
	Name  string
	Lavfi string
}

// Fixtures covers the three content types from SPEC 10. The screencast is the
// hard case and the dominant one in e-learning: flat colour and sharp edges
// give a pixel-domain mark almost nowhere to hide, and the encoder spends very
// few bits there.
func Fixtures(width, height, fps, seconds int) []Fixture {
	geom := fmt.Sprintf("%dx%d", width, height)
	return []Fixture{
		{
			Name:  "high_motion",
			Lavfi: fmt.Sprintf("testsrc2=size=%s:rate=%d:duration=%d", geom, fps, seconds),
		},
		{
			Name: "talking_head",
			Lavfi: fmt.Sprintf("gradients=size=%s:rate=%d:duration=%d:speed=0.05", geom, fps, seconds) +
				",noise=alls=6:allf=t+u,vignette,gblur=sigma=1.5",
		},
		{Name: "screencast", Lavfi: screencastGraph(width, height, fps, seconds)},
	}
}

// A deck of static slides compresses to a few kbps, which makes "60% of source
// bitrate" a meaningless attack. Real capture scrolls and carries a cursor, so
// the fixture does too.
func screencastGraph(width, height, fps, seconds int) string {
	canvasH := height + height/2
	var b strings.Builder
	fmt.Fprintf(&b, "color=c=0xF2F2F2:s=%dx%d:r=%d:d=%d", width, canvasH, fps, seconds)
	fmt.Fprintf(&b, ",drawbox=x=0:y=0:w=%d:h=%d:color=0x2B3A55:t=fill", width, height/12)
	r := 1
	for i := 0; i < 30; i++ {
		r = (r*1103515245 + 12345) & 0x7fffffff
		y := height/6 + i*(height/14)
		if y > canvasH-height/12 {
			break
		}
		switch i % 7 {
		case 3:
			fmt.Fprintf(&b, ",drawbox=x=%d:y=%d:w=%d:h=%d:color=0xE4E9F0:t=fill",
				width/10, y, width*3/4, height/12)
			fmt.Fprintf(&b, ",drawbox=x=%d:y=%d:w=%d:h=%d:color=0x3A4A5A:t=fill",
				width/8, y+height/40, width/4+r%(width/3), height/48)
		default:
			fmt.Fprintf(&b, ",drawbox=x=%d:y=%d:w=%d:h=%d:color=0x505050:t=fill",
				width/10, y, width/3+r%(width/2), height/48)
		}
	}
	scroll := fmt.Sprintf("if(lt(t,%d),0,if(lt(t,%d),(t-%d)*%d,%d))",
		seconds/3, 2*seconds/3, seconds/3, height/4, (height/4)*(seconds/3))
	fmt.Fprintf(&b, ",crop=%d:%d:0:'min(%d,%s)'", width, height, canvasH-height, scroll)
	fmt.Fprintf(&b, ",drawbox=x='%d+%d*abs(sin(t*0.9))':y='%d+%d*abs(cos(t*0.6))':w=%d:h=%d:color=0x101010:t=fill",
		width/6, width/2, height/5, height/2, width/100, height/40)
	return b.String()
}

func (f Fixture) Render(ctx context.Context, dir string, keyframeInterval float64) (string, error) {
	dst := filepath.Join(dir, f.Name+".mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", f.Lavfi,
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", keyframeInterval),
		"-sc_threshold", "0",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p", dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("eval: render fixture %s: %w: %s", f.Name, err, stderr.String())
	}
	return dst, nil
}
