package app

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// EnableBudapestBackground is the central compile-time toggle for the animated
// Budapest Chain Bridge background projection behind the shell. Set to false
// to completely disable the bitmap renderer, background colour shifting, and
// animation ticker.
const EnableBudapestBackground = true

const (
	budapestW          = 399
	budapestH          = 501
	budapestSkyH       = 160
	budapestShiftAlpha = 0.20
	budapestFrameRate  = 33 * time.Millisecond
)

//go:embed lanchid.png
var lanchidPNG []byte

type budapestPoint struct {
	x, y float64
}

type budapestPose struct {
	x, y, angle float64
}

type budapestSpline struct {
	points      []budapestPoint
	lengths     []float64
	totalLength float64
}

type budapestCar struct {
	lane      *budapestSpline
	dist      float64
	baseSpeed float64
	isTaxi    bool
}

type budapestLight struct {
	x, y, r       float64
	phase, phase2 float64
	speed         float64
}

type budapestGlint struct {
	x, y, w      float64
	phase, speed float64
	isGold       bool
}

var (
	budapestInitOnce sync.Once
	budapestBaseRGBA []uint8 // 399 * 501 * 4
	budapestSkyRGBA  []uint8 // 399 * 160 * 4 (with alpha feathering & cathedral cutout)
	budapestLane1    budapestSpline
	budapestLane2    budapestSpline
	budapestLights   []budapestLight
	budapestGlints   []budapestGlint
)

func initBudapestAssets() {
	budapestInitOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(lanchidPNG))
		if err != nil {
			budapestBaseRGBA = make([]uint8, budapestW*budapestH*4)
			budapestSkyRGBA = make([]uint8, budapestW*budapestSkyH*4)
			return
		}
		budapestBaseRGBA = imageToRGBA(img, budapestW, budapestH)
		budapestSkyRGBA = buildSkyBuffer(budapestBaseRGBA)
		budapestLane1 = createBudapestSpline([]budapestPoint{
			{240, 205}, {219, 219}, {186, 245}, {170, 259},
			{153, 273}, {126, 297}, {111, 311}, {95, 325},
			{78, 339}, {56, 353}, {22, 367}, {0, 371},
		})
		budapestLane2 = createBudapestSpline([]budapestPoint{
			{167, 499}, {182, 484}, {194, 469}, {207, 454},
			{205, 439}, {181, 424}, {138, 409}, {87, 381},
			{83, 366}, {95, 351}, {110, 336}, {128, 321},
			{145, 305}, {163, 290}, {179, 275}, {198, 258},
			{218, 242}, {238, 226}, {250, 216},
		})
		budapestLights = buildBudapestLights()
		budapestGlints = buildBudapestGlints()
	})
}

func imageToRGBA(img image.Image, w, h int) []uint8 {
	out := make([]uint8, w*h*4)
	b := img.Bounds()
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y
		for x := 0; x < w; x++ {
			sx := b.Min.X + x
			r, g, bl, a := img.At(sx, sy).RGBA()
			idx := (y*w + x) * 4
			if a > 0 && a < 0xffff {
				out[idx+0] = uint8(min(255, int(r*0xffff/a)>>8))
				out[idx+1] = uint8(min(255, int(g*0xffff/a)>>8))
				out[idx+2] = uint8(min(255, int(bl*0xffff/a)>>8))
			} else {
				out[idx+0] = uint8(r >> 8)
				out[idx+1] = uint8(g >> 8)
				out[idx+2] = uint8(bl >> 8)
			}
			out[idx+3] = uint8(a >> 8)
		}
	}
	return out
}

func buildSkyBuffer(base []uint8) []uint8 {
	sky := make([]uint8, budapestW*budapestSkyH*4)
	copy(sky, base[:budapestW*budapestSkyH*4])

	for y := 0; y < budapestSkyH; y++ {
		vf := 1.0
		if fy := float64(y) / float64(budapestSkyH); fy > 0.76 {
			vf = max(0, 1.0-(fy-0.76)/(1.0-0.76))
		}
		for x := 0; x < budapestW; x++ {
			hf := 1.0
			fx := float64(x) / float64(budapestW)
			if fx < 0.10 {
				hf = fx / 0.10
			} else if fx > 0.90 {
				hf = (1.0 - fx) / 0.10
			}
			alpha := hf * vf

			// Radial cathedral cutout centered at (325, 148), rInner=34, rOuter=66
			if x >= 245 && y >= 80 {
				dist := math.Hypot(float64(x)-325.0, float64(y)-148.0)
				if dist < 66.0 {
					var cut float64
					if dist <= 34.0 {
						cut = 1.0
					} else {
						t := (dist - 34.0) / (66.0 - 34.0)
						if t <= 0.6 {
							cut = 1.0 - (t/0.6)*0.1
						} else {
							cut = 0.9 * (1.0 - (t-0.6)/0.4)
						}
					}
					alpha *= (1.0 - cut)
				}
			}

			// Lower-middle skyline feather (x: 135..399, y: 118..152)
			if x >= 135 && y >= 118 {
				t := min(1.0, float64(y-118)/float64(152-118))
				alpha *= (1.0 - t)
			}

			idx := (y*budapestW + x) * 4
			sky[idx+3] = uint8(clampFloat(alpha*255.0, 0, 255))
		}
	}
	return sky
}

func createBudapestSpline(pts []budapestPoint) budapestSpline {
	totalSteps := len(pts) * 60
	points := make([]budapestPoint, 0, totalSteps+1)
	for i := 0; i <= totalSteps; i++ {
		t := float64(i) / float64(totalSteps)
		points = append(points, catmullRomPoint(pts, t))
	}
	lengths := make([]float64, len(points))
	for i := 1; i < len(points); i++ {
		dx := points[i].x - points[i-1].x
		dy := points[i].y - points[i-1].y
		lengths[i] = lengths[i-1] + math.Hypot(dx, dy)
	}
	return budapestSpline{
		points:      points,
		lengths:     lengths,
		totalLength: lengths[len(lengths)-1],
	}
}

func catmullRomPoint(pts []budapestPoint, t float64) budapestPoint {
	n := len(pts) - 1
	p := t * float64(n)
	ip := int(math.Floor(p))
	i0 := max(0, ip-1)
	i1 := min(n, ip)
	i2 := min(n, i1+1)
	i3 := min(n, i1+2)
	u := p - float64(i1)
	u2 := u * u
	u3 := u2 * u
	cr := func(p0, p1, p2, p3 float64) float64 {
		return 0.5 * ((2 * p1) + (-p0+p2)*u + (2*p0-5*p1+4*p2-p3)*u2 + (-p0+3*p1-3*p2+p3)*u3)
	}
	return budapestPoint{
		x: cr(pts[i0].x, pts[i1].x, pts[i2].x, pts[i3].x),
		y: cr(pts[i0].y, pts[i1].y, pts[i2].y, pts[i3].y),
	}
}

func sampleBudapestSpline(s *budapestSpline, dist float64) budapestPose {
	var d float64
	if dist >= s.totalLength {
		d = s.totalLength
	} else {
		d = math.Mod(math.Mod(dist, s.totalLength)+s.totalLength, s.totalLength)
	}
	idx := searchSplineIndex(s.lengths, d)
	segLen := s.lengths[idx] - s.lengths[idx-1]
	frac := 0.0
	if segLen > 0 {
		frac = (d - s.lengths[idx-1]) / segLen
	}
	p0 := s.points[idx-1]
	p1 := s.points[idx]
	x := p0.x + (p1.x-p0.x)*frac
	y := p0.y + (p1.y-p0.y)*frac

	aIdx := searchSplineIndex(s.lengths, min(s.totalLength, d+2))
	bIdx := max(0, searchSplineIndex(s.lengths, max(0, d-2))-1)
	ap := s.points[aIdx]
	bp := s.points[bIdx]
	return budapestPose{
		x:     x,
		y:     y,
		angle: math.Atan2(ap.y-bp.y, ap.x-bp.x),
	}
}

func searchSplineIndex(lengths []float64, d float64) int {
	low, high := 0, len(lengths)-1
	for low <= high {
		mid := (low + high) >> 1
		if lengths[mid] < d {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return max(1, min(len(lengths)-1, low))
}

func buildBudapestLights() []budapestLight {
	realBridge := [][2]float64{
		{254, 200}, {258, 200}, {276, 200}, {250, 201}, {262, 201}, {246, 202}, {256, 203}, {279, 203}, {259, 204},
		{237, 207}, {249, 208}, {256, 209}, {283, 209}, {253, 211}, {238, 212}, {243, 212}, {249, 212}, {232, 214},
		{274, 214}, {241, 215}, {269, 215}, {237, 216}, {247, 217}, {235, 219}, {239, 219}, {230, 220}, {261, 220},
		{243, 221}, {228, 224}, {232, 224}, {255, 224}, {238, 225}, {220, 227}, {233, 227}, {216, 228}, {249, 229},
		{218, 231}, {223, 231}, {245, 233}, {211, 234}, {207, 240}, {210, 241}, {231, 241}, {236, 241}, {204, 242},
		{225, 246}, {202, 251}, {184, 258}, {181, 263}, {187, 263}, {228, 265}, {215, 266}, {218, 266}, {222, 266},
		{168, 274}, {171, 278}, {218, 279}, {179, 280}, {201, 284}, {199, 287}, {153, 293}, {144, 295}, {151, 297},
		{141, 298}, {148, 302}, {178, 305}, {142, 308}, {175, 308}, {169, 315}, {125, 316}, {119, 321}, {127, 321},
		{123, 322}, {132, 324}, {161, 324}, {149, 326}, {128, 329}, {155, 331}, {151, 336}, {120, 341}, {125, 342},
		{172, 345}, {175, 345}, {119, 350}, {123, 358}, {127, 364}, {141, 377},
	}
	realBudaLanterns := [][2]float64{
		{282, 330}, {299, 330}, {263, 340}, {272, 376},
	}
	realCathedral := [][2]float64{
		{321, 135}, {332, 135}, {295, 136}, {345, 136}, {348, 136}, {321, 138}, {326, 138}, {332, 138}, {347, 139},
		{315, 142}, {323, 142}, {306, 160}, {313, 160}, {320, 160}, {323, 160}, {326, 160}, {332, 160}, {302, 164},
	}

	out := make([]budapestLight, 0, len(realBridge)+len(realBudaLanterns)+len(realCathedral))
	twoPi := math.Pi * 2
	for i, pt := range realBridge {
		r := 1.9
		switch {
		case pt[1] < 230:
			r = 1.0
		case pt[1] < 310:
			r = 1.35
		case pt[1] < 370:
			r = 1.6
		}
		out = append(out, budapestLight{
			x:      pt[0],
			y:      pt[1],
			r:      r,
			phase:  math.Mod(float64(i)*0.73, twoPi),
			phase2: math.Mod(float64(i)*1.67, twoPi),
			speed:  1.4 + float64(i%7)*0.28,
		})
	}
	for i, pt := range realBudaLanterns {
		out = append(out, budapestLight{
			x:      pt[0],
			y:      pt[1],
			r:      3.2,
			phase:  math.Mod(float64(i)*1.57, twoPi),
			phase2: math.Mod(float64(i)*2.31, twoPi),
			speed:  1.6 + float64(i)*0.3,
		})
	}
	for i, pt := range realCathedral {
		out = append(out, budapestLight{
			x:      pt[0],
			y:      pt[1],
			r:      1.1,
			phase:  math.Mod(float64(i)*0.81, twoPi),
			phase2: math.Mod(float64(i)*1.93, twoPi),
			speed:  1.3 + float64(i%5)*0.25,
		})
	}
	return out
}

func buildBudapestGlints() []budapestGlint {
	rng := rand.New(rand.NewSource(18491120)) // deterministic seed
	glints := make([]budapestGlint, 140)
	for i := range glints {
		isLeft := rng.Float64() < 0.35
		var x, y float64
		if isLeft {
			x = rng.Float64() * 128.0
			y = 222.0 + rng.Float64()*50.0
		} else {
			x = 228.0 + rng.Float64()*170.0
			y = 222.0 + rng.Float64()*110.0
		}
		glints[i] = budapestGlint{
			x:      x,
			y:      y,
			w:      3.0 + rng.Float64()*4.5,
			phase:  rng.Float64() * math.Pi * 2,
			speed:  1.5 + rng.Float64()*2.2,
			isGold: rng.Float64() < 0.35,
		}
	}
	return glints
}

// budapestScene holds per-AppWidget animation state and reusable frame buffers.
type budapestScene struct {
	mu        sync.Mutex
	frame     []uint8
	cars      [4]budapestCar
	startTime time.Time
	prevTime  float64
	hasPrev   bool
}

func newBudapestScene() *budapestScene {
	initBudapestAssets()
	s := &budapestScene{
		frame:     make([]uint8, budapestW*budapestH*4),
		startTime: time.Now(),
	}
	s.cars = [4]budapestCar{
		{lane: &budapestLane1, dist: budapestLane1.totalLength * 0.10, baseSpeed: 28, isTaxi: true},
		{lane: &budapestLane1, dist: budapestLane1.totalLength * 0.60, baseSpeed: 30, isTaxi: true},
		{lane: &budapestLane2, dist: budapestLane2.totalLength * 0.15, baseSpeed: 29, isTaxi: true},
		{lane: &budapestLane2, dist: budapestLane2.totalLength * 0.65, baseSpeed: 31, isTaxi: true},
	}
	return s
}

func (s *budapestScene) renderAt(timeSeconds float64) []uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()

	dt := 0.016
	if s.hasPrev {
		dt = timeSeconds - s.prevTime
		if dt < 0 {
			dt = 0
		} else if dt > 0.05 {
			dt = 0.05
		}
	}
	s.prevTime = timeSeconds
	s.hasPrev = true

	// 1. Base plate
	copy(s.frame, budapestBaseRGBA)

	// 2. Clouds & sunset breathing
	s.drawClouds(timeSeconds)

	// 3. Danube river shimmer
	s.drawRiverShimmer(timeSeconds)

	// 4. Moving 2.5D Budapest yellow taxis
	for i := range s.cars {
		car := &s.cars[i]
		p := sampleBudapestSpline(car.lane, car.dist)
		depthFactor := clampFloat((p.y-200.0)/295.0, 0, 1)
		scale := 0.28 + 0.72*math.Pow(depthFactor, 1.6)
		speed := car.baseSpeed * (0.35 + 0.65*depthFactor)

		if car.lane.totalLength > 0 {
			car.dist = math.Mod(car.dist+speed*dt, car.lane.totalLength)
		}
		motionLen := max(1.5, speed*dt*2.2)
		edgeDist := min(car.dist, car.lane.totalLength-car.dist)
		fadeAlpha := min(1.0, edgeDist/22.0)

		// Arch occlusion gap (y in [246..276])
		if p.y >= 246.0 && p.y <= 276.0 {
			continue
		}
		if fadeAlpha > 0.02 {
			s.drawCar(p, scale, car.isTaxi, motionLen, fadeAlpha)
		}
	}

	// 5. Restore sharp stone archway (136, 215, 80x85)
	for y := 215; y < 215+85 && y < budapestH; y++ {
		rowStart := (y*budapestW + 136) * 4
		rowEnd := (y*budapestW + min(budapestW, 136+80)) * 4
		copy(s.frame[rowStart:rowEnd], budapestBaseRGBA[rowStart:rowEnd])
	}

	// 6. Glimmering stationary bridge & skyline lights
	s.drawGlimmeringLights(timeSeconds)

	return s.frame
}

func (s *budapestScene) drawClouds(timeSeconds float64) {
	const T = 13.0
	phase1 := math.Mod(timeSeconds/T, 1.0)
	if phase1 < 0 {
		phase1 += 1.0
	}
	phase2 := math.Mod(timeSeconds/T+0.5, 1.0)
	if phase2 < 0 {
		phase2 += 1.0
	}

	s1 := math.Sin(phase1 * math.Pi)
	s2 := math.Sin(phase2 * math.Pi)
	w1 := s1 * s1
	w2 := s2 * s2

	const maxShift = 24.0
	off1 := int(math.Round((phase1 - 0.5) * maxShift * 2.0))
	off2 := int(math.Round((phase2 - 0.5) * maxShift * 2.0))

	for y := 0; y < budapestSkyH; y++ {
		for x := 0; x < budapestW; x++ {
			var rMix, gMix, bMix, aMix float64
			if x1 := x - off1; x1 >= 0 && x1 < budapestW {
				idx1 := (y*budapestW + x1) * 4
				a1 := (float64(budapestSkyRGBA[idx1+3]) / 255.0) * w1
				rMix += float64(budapestSkyRGBA[idx1+0]) * a1
				gMix += float64(budapestSkyRGBA[idx1+1]) * a1
				bMix += float64(budapestSkyRGBA[idx1+2]) * a1
				aMix += a1
			}
			if x2 := x - off2; x2 >= 0 && x2 < budapestW {
				idx2 := (y*budapestW + x2) * 4
				a2 := (float64(budapestSkyRGBA[idx2+3]) / 255.0) * w2
				rMix += float64(budapestSkyRGBA[idx2+0]) * a2
				gMix += float64(budapestSkyRGBA[idx2+1]) * a2
				bMix += float64(budapestSkyRGBA[idx2+2]) * a2
				aMix += a2
			}
			if aMix > 0.001 {
				outA := min(1.0, aMix*0.92)
				invA := 1.0 - outA
				dstIdx := (y*budapestW + x) * 4
				s.frame[dstIdx+0] = uint8(clampFloat(float64(s.frame[dstIdx+0])*invA+rMix*0.92, 0, 255))
				s.frame[dstIdx+1] = uint8(clampFloat(float64(s.frame[dstIdx+1])*invA+gMix*0.92, 0, 255))
				s.frame[dstIdx+2] = uint8(clampFloat(float64(s.frame[dstIdx+2])*invA+bMix*0.92, 0, 255))
			}
		}
	}

	// Sunset dusk light breathing on left horizon (screen blend)
	breath := 0.5 + 0.5*math.Sin(timeSeconds*0.45)
	a0 := 0.24 + 0.14*breath
	aMid := 0.14 + 0.08*breath
	for y := 0; y < 165; y++ {
		for x := 0; x < 140; x++ {
			dist := math.Hypot(float64(x)-30.0, float64(y)-110.0)
			if dist >= 125.0 {
				continue
			}
			t := max(0, (dist-8.0)/(125.0-8.0))
			var sr, sg, sb, sa float64
			if t <= 0.45 {
				u := t / 0.45
				sr = 255.0 + (235.0-255.0)*u
				sg = 185.0 + (135.0-185.0)*u
				sb = 90.0 + (55.0-90.0)*u
				sa = a0 + (aMid-a0)*u
			} else {
				u := (t - 0.45) / 0.55
				sr = 235.0 + (120.0-235.0)*u
				sg = 135.0 + (60.0-135.0)*u
				sb = 55.0 + (30.0-55.0)*u
				sa = aMid * (1.0 - u)
			}
			screenBlendPixel(s.frame, x, y, sr*sa, sg*sa, sb*sa)
		}
	}
}

func (s *budapestScene) drawRiverShimmer(timeSeconds float64) {
	for _, glint := range budapestGlints {
		rawWave := math.Sin(timeSeconds*glint.speed + glint.phase)
		if rawWave <= 0 {
			continue
		}
		pulse := rawWave * rawWave * rawWave * rawWave
		if pulse <= 0.03 {
			continue
		}
		var cx float64
		if glint.x < 130.0 {
			cx = math.Mod(glint.x+timeSeconds*2.2, 130.0)
		} else {
			cx = 228.0 + math.Mod(glint.x-228.0+timeSeconds*2.2, 170.0)
		}
		cy := glint.y + math.Sin(timeSeconds*0.7+glint.phase)*0.6

		var cr, cg, cb, baseA float64
		if glint.isGold {
			cr, cg, cb = 255, 215, 125
			baseA = pulse * 0.55
		} else {
			cr, cg, cb = 180, 215, 245
			baseA = pulse * 0.42
		}

		// Soft blurred ellipse (accounting for blur(1.5px))
		rx := glint.w*0.5 + 1.5
		ry := 2.2
		minX := max(0, int(math.Floor(cx-rx)))
		maxX := min(budapestW-1, int(math.Ceil(cx+rx)))
		minY := max(0, int(math.Floor(cy-ry)))
		maxY := min(budapestH-1, int(math.Ceil(cy+ry)))

		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				if !isInWaterRegion(x, y) {
					continue
				}
				nx := (float64(x) - cx) / rx
				ny := (float64(y) - cy) / ry
				d2 := nx*nx + ny*ny
				if d2 >= 1.0 {
					continue
				}
				w := (1.0 - d2) * baseA
				additiveBlendPixel(s.frame, x, y, cr*w, cg*w, cb*w)
			}
		}
	}
}

func isInWaterRegion(x, y int) bool {
	// Left river: rect(0, 222, 130, 52)
	if x >= 0 && x < 130 && y >= 222 && y <= 274 {
		return true
	}
	// Right river: x >= 178, y in [218..388], outside bridge deck
	if x >= 228 && x < budapestW && y >= 220 && y <= 345 {
		return true
	}
	if x >= 180 && x < 228 && y >= 285 && y <= 375 {
		return true
	}
	return false
}

func (s *budapestScene) drawCar(p budapestPose, scale float64, isTaxi bool, motionLen float64, fadeAlpha float64) {
	L := 13.5 * scale
	W := 5.8 * scale
	cosA := math.Cos(p.angle)
	sinA := math.Sin(p.angle)
	const tilt = 0.54

	// Helper to project a local rotated box at screen vertical offset zOffset
	drawSliceBox := func(zOffset, uMin, uMax, vMin, vMax float64, r, g, b uint8, alpha float64) {
		a := alpha * fadeAlpha
		if a <= 0.01 {
			return
		}
		cyScreen := p.y - zOffset*scale
		pad := max(L, W)*1.5 + 3.0
		minX := max(0, int(math.Floor(p.x-pad)))
		maxX := min(budapestW-1, int(math.Ceil(p.x+pad)))
		minY := max(0, int(math.Floor(cyScreen-pad*tilt-2)))
		maxY := min(budapestH-1, int(math.Ceil(cyScreen+pad*tilt+2)))

		softEdge := max(0.6, 1.1*scale)
		for y := minY; y <= maxY; y++ {
			dy := (float64(y) - cyScreen) / tilt
			for x := minX; x <= maxX; x++ {
				dx := float64(x) - p.x
				u := dx*cosA + dy*sinA
				v := -dx*sinA + dy*cosA
				du := max(0.0, max(uMin-u, u-uMax))
				dv := max(0.0, max(vMin-v, v-vMax))
				dist := math.Hypot(du, dv)
				if dist >= softEdge {
					continue
				}
				w := (1.0 - dist/softEdge) * a
				alphaBlendPixel(s.frame, x, y, r, g, b, w)
			}
		}
	}

	// 1. Ground level (z=0): translucent headlight cone + contact shadow
	beamLen := 32.0 * scale
	beamW := 18.0 * scale
	padBeam := beamLen + 4.0
	minBX := max(0, int(math.Floor(p.x-padBeam)))
	maxBX := min(budapestW-1, int(math.Ceil(p.x+padBeam)))
	minBY := max(0, int(math.Floor(p.y-padBeam*tilt)))
	maxBY := min(budapestH-1, int(math.Ceil(p.y+padBeam*tilt)))
	for y := minBY; y <= maxBY; y++ {
		dy := (float64(y) - p.y) / tilt
		for x := minBX; x <= maxBX; x++ {
			dx := float64(x) - p.x
			u := dx*cosA + dy*sinA
			v := -dx*sinA + dy*cosA
			if u < L*0.7 || u > beamLen {
				continue
			}
			halfSpan := W*0.55 + (beamW*0.65-W*0.55)*((u-L*0.7)/(beamLen-L*0.7))
			if math.Abs(v) > halfSpan {
				continue
			}
			t := (u - L*0.7) / (beamLen - L*0.7)
			edgeFactor := 1.0 - (math.Abs(v) / halfSpan)
			beamAlpha := 0.18 * (1.0 - t) * (1.0 - t) * edgeFactor * fadeAlpha
			additiveBlendPixel(s.frame, x, y, 255*beamAlpha, 225*beamAlpha, 140*beamAlpha)
		}
	}

	// Contact shadow at z=0
	drawSliceBox(0, -L*1.04, L*1.04, -W*1.04, W*1.04, 8, 11, 15, 0.90)

	// 2. Lower body / doors (z=1.6) with motion blur passes
	step := min(5.0*scale, (motionLen*0.55)/3.0)
	for k := 2; k >= 1; k-- {
		off := float64(k) * step
		if isTaxi {
			drawSliceBox(1.6, -L-off, L-off, -W, W, 220, 160, 14, 0.28)
		} else {
			drawSliceBox(1.6, -L-off, L-off, -W, W, 38, 44, 50, 0.28)
		}
	}
	if isTaxi {
		drawSliceBox(1.6, -L, L, -W, W, 204, 142, 10, 0.95)
	} else {
		drawSliceBox(1.6, -L, L, -W, W, 40, 48, 56, 0.95)
	}

	// 3. Beltline / hood & trunk deck (z=3.1)
	if isTaxi {
		drawSliceBox(3.1, -L*0.96, L*0.96, -W*0.95, W*0.95, 245, 190, 24, 0.98)
	} else {
		drawSliceBox(3.1, -L*0.96, L*0.96, -W*0.95, W*0.95, 56, 66, 76, 0.98)
	}

	// Headlamps & Taillamps at z=3.1
	drawLamp := func(uPos, vPos, rad, r, g, b float64) {
		sx := p.x + uPos*cosA - vPos*sinA
		sy := (p.y - 3.1*scale) + (uPos*sinA+vPos*cosA)*tilt
		drawAdditiveGlow(s.frame, sx, sy, rad*scale, r, g, b, fadeAlpha)
	}
	drawLamp(L*0.95, -W*0.62, 3.2, 255, 230, 160)
	drawLamp(L*0.95, W*0.62, 3.2, 255, 230, 160)
	drawLamp(-L*0.96, -W*0.62, 3.0, 255, 45, 20)
	drawLamp(-L*0.96, W*0.62, 3.0, 255, 45, 20)

	// 4. Dark cabin glass (z=4.6)
	drawSliceBox(4.6, -L*0.54, L*0.48, -W*0.86, W*0.86, 24, 34, 42, 0.96)

	// 5. Roof cap & Taxi lightbox (z=6.0)
	if isTaxi {
		drawSliceBox(6.0, -L*0.42, L*0.36, -W*0.78, W*0.78, 247, 202, 46, 0.98)
		drawSliceBox(6.3, -L*0.10, L*0.10, -W*0.38, W*0.38, 255, 250, 232, 0.98)
	}
}

func (s *budapestScene) drawGlimmeringLights(timeSeconds float64) {
	for _, l := range budapestLights {
		wave1 := math.Sin(timeSeconds*l.speed + l.phase)
		wave2 := math.Sin(timeSeconds*l.speed*2.3 + l.phase2)
		intensity := 0.55 + 0.35*wave1 + 0.18*wave2
		if intensity <= 0.02 {
			continue
		}
		currentR := l.r * (0.95 + 0.32*wave1)
		rad := currentR * 2.2
		minX := max(0, int(math.Floor(l.x-rad)))
		maxX := min(budapestW-1, int(math.Ceil(l.x+rad)))
		minY := max(0, int(math.Floor(l.y-rad)))
		maxY := min(budapestH-1, int(math.Ceil(l.y+rad)))

		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				dist := math.Hypot(float64(x)-l.x, float64(y)-l.y)
				if dist >= rad {
					continue
				}
				t := dist / rad
				var r, g, b, a float64
				if t <= 0.3 {
					u := t / 0.3
					r = 255.0
					g = 255.0 + (218.0-255.0)*u
					b = 235.0 + (125.0-235.0)*u
					a = (0.95 + (0.68-0.95)*u) * intensity
				} else if t <= 0.65 {
					u := (t - 0.3) / 0.35
					r = 255.0
					g = 218.0 + (160.0-218.0)*u
					b = 125.0 + (45.0-125.0)*u
					a = (0.68 + (0.28-0.68)*u) * intensity
				} else {
					u := (t - 0.65) / 0.35
					r = 255.0
					g = 160.0 + (120.0-160.0)*u
					b = 45.0 + (20.0-45.0)*u
					a = 0.28 * (1.0 - u) * intensity
				}
				additiveBlendPixel(s.frame, x, y, r*a, g*a, b*a)
			}
		}
	}
}

func drawAdditiveGlow(frame []uint8, cx, cy, rad, r, g, b, fadeAlpha float64) {
	if rad <= 0.2 {
		return
	}
	minX := max(0, int(math.Floor(cx-rad)))
	maxX := min(budapestW-1, int(math.Ceil(cx+rad)))
	minY := max(0, int(math.Floor(cy-rad)))
	maxY := min(budapestH-1, int(math.Ceil(cy+rad)))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			if d >= rad {
				continue
			}
			w := (1.0 - d/rad) * (1.0 - d/rad) * fadeAlpha
			additiveBlendPixel(frame, x, y, r*w, g*w, b*w)
		}
	}
}

func additiveBlendPixel(frame []uint8, x, y int, dr, dg, db float64) {
	idx := (y*budapestW + x) * 4
	frame[idx+0] = uint8(min(255, int(float64(frame[idx+0])+dr)))
	frame[idx+1] = uint8(min(255, int(float64(frame[idx+1])+dg)))
	frame[idx+2] = uint8(min(255, int(float64(frame[idx+2])+db)))
}

func screenBlendPixel(frame []uint8, x, y int, sr, sg, sb float64) {
	idx := (y*budapestW + x) * 4
	sc := func(dst uint8, src float64) uint8 {
		d := float64(dst) / 255.0
		s := clampFloat(src/255.0, 0, 1)
		return uint8(clampFloat((1.0-(1.0-d)*(1.0-s))*255.0, 0, 255))
	}
	frame[idx+0] = sc(frame[idx+0], sr)
	frame[idx+1] = sc(frame[idx+1], sg)
	frame[idx+2] = sc(frame[idx+2], sb)
}

func alphaBlendPixel(frame []uint8, x, y int, r, g, b uint8, a float64) {
	a = clampFloat(a, 0, 1)
	inv := 1.0 - a
	idx := (y*budapestW + x) * 4
	frame[idx+0] = uint8(float64(frame[idx+0])*inv + float64(r)*a)
	frame[idx+1] = uint8(float64(frame[idx+1])*inv + float64(g)*a)
	frame[idx+2] = uint8(float64(frame[idx+2])*inv + float64(b)*a)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// centeredCropBounds computes the centered rectangle inside the 399x501 bitmap
// proportionate to a terminal of termW columns by termH rows (accounting for
// ~1:2 width:height character cell aspect ratio).
func centeredCropBounds(termW, termH int) (cropX, cropY, cropW, cropH float64) {
	if termW <= 0 {
		termW = 80
	}
	if termH <= 0 {
		termH = 24
	}
	// Monospace character cells are ~2x as tall as they are wide.
	targetAspect := float64(termW) / float64(termH*2)
	imgAspect := float64(budapestW) / float64(budapestH)

	if targetAspect >= imgAspect {
		// Terminal is wider than the portrait image: span full width, crop top & bottom.
		cropW = float64(budapestW)
		cropH = cropW / targetAspect
		cropX = 0
		cropY = (float64(budapestH) - cropH) * 0.5
	} else {
		// Terminal is taller/narrower than the image: span full height, crop left & right.
		cropH = float64(budapestH)
		cropW = cropH * targetAspect
		cropX = (float64(budapestW) - cropW) * 0.5
		cropY = 0
	}
	return cropX, cropY, cropW, cropH
}

// applyBudapestProjection shifts the background colours of cells in buf slightly
// towards the average pixel colour of the corresponding cell in the centered
// Budapest frame crop for a terminal of size (termW, termH).
func (s *budapestScene) applyBudapestProjection(buf *terminal.CellBuf, termW, termH int, theme ui.Theme) {
	if !EnableBudapestBackground || s == nil || buf == nil || buf.Width <= 0 || buf.Height <= 0 {
		return
	}
	if termW <= 0 {
		termW = buf.Width
	}
	if termH <= 0 {
		termH = buf.Height
	}
	elapsed := time.Since(s.startTime).Seconds()
	frame := s.renderAt(elapsed)

	cropX, cropY, cropW, cropH := centeredCropBounds(termW, termH)
	cellW := cropW / float64(termW)
	cellH := cropH / float64(termH)

	defaultBG := theme.EffectiveBackground()
	rows := min(buf.Height, termH)
	cols := min(buf.Width, termW)

	for row := 0; row < rows; row++ {
		y0 := int(math.Floor(cropY + float64(row)*cellH))
		y1 := int(math.Ceil(cropY + float64(row+1)*cellH))
		y0 = max(0, min(budapestH-1, y0))
		y1 = max(y0+1, min(budapestH, y1))

		for col := 0; col < cols; col++ {
			x0 := int(math.Floor(cropX + float64(col)*cellW))
			x1 := int(math.Ceil(cropX + float64(col+1)*cellW))
			x0 = max(0, min(budapestW-1, x0))
			x1 = max(x0+1, min(budapestW, x1))

			var sumR, sumG, sumB, count int
			for py := y0; py < y1; py++ {
				rowOff := py * budapestW * 4
				for px := x0; px < x1; px++ {
					idx := rowOff + px*4
					sumR += int(frame[idx+0])
					sumG += int(frame[idx+1])
					sumB += int(frame[idx+2])
					count++
				}
			}
			if count == 0 {
				continue
			}
			avgR := float64(sumR) / float64(count)
			avgG := float64(sumG) / float64(count)
			avgB := float64(sumB) / float64(count)

			cell := buf.Get(col, row)
			style := cell.Style
			attrs := style.Attributes()
			bg := style.Background()
			if attrs&terminal.CharResetBackgroundDefault != 0 {
				bg = defaultBG
				attrs &^= terminal.CharResetBackgroundDefault
			}
			fg := style.Foreground()

			bgR := float64((bg >> 16) & 0xFF)
			bgG := float64((bg >> 8) & 0xFF)
			bgB := float64(bg & 0xFF)

			newR := uint8(clampFloat(math.Round(bgR*(1.0-budapestShiftAlpha)+avgR*budapestShiftAlpha), 0, 255))
			newG := uint8(clampFloat(math.Round(bgG*(1.0-budapestShiftAlpha)+avgG*budapestShiftAlpha), 0, 255))
			newB := uint8(clampFloat(math.Round(bgB*(1.0-budapestShiftAlpha)+avgB*budapestShiftAlpha), 0, 255))

			shiftedBG := terminal.Color(uint32(newR)<<16 | uint32(newG)<<8 | uint32(newB))
			cell.Style = terminal.NewStyle(fg, shiftedBG, attrs)
			buf.Set(col, row, cell)
		}
	}
}
