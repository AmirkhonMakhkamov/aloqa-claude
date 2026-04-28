package sfu

import (
	"math"
	"strings"
	"sync"
	"time"
)

type DeviceClass string

const (
	DeviceClassUnknown DeviceClass = ""
	DeviceClassLegacy  DeviceClass = "legacy"
	DeviceClassLow     DeviceClass = "low"
	DeviceClassMid     DeviceClass = "mid"
	DeviceClassHigh    DeviceClass = "high"
)

type NetworkGrade string

const (
	NetworkGradeExcellent NetworkGrade = "excellent"
	NetworkGradeGood      NetworkGrade = "good"
	NetworkGradeFair      NetworkGrade = "fair"
	NetworkGradePoor      NetworkGrade = "poor"
	NetworkGradeCritical  NetworkGrade = "critical"
)

// AdaptiveOptions controls the network-driven video adaptation policy.
// Values are intentionally conservative: the engine protects audio first,
// degrades quickly, and only upgrades after the network is stable.
type AdaptiveOptions struct {
	LowLayerMinKbps         int
	MediumLayerMinKbps      int
	HighLayerMinKbps        int
	CriticalLayerKbps       int
	MinUpswitchInterval     time.Duration
	MinDownswitchInterval   time.Duration
	GoodSamplesForUpgrade   int
	PoorSamplesForDowngrade int
	EWMAAlpha               float64
}

func (o AdaptiveOptions) withDefaults() AdaptiveOptions {
	if o.LowLayerMinKbps <= 0 {
		o.LowLayerMinKbps = 180
	}
	if o.MediumLayerMinKbps <= 0 {
		o.MediumLayerMinKbps = 650
	}
	if o.HighLayerMinKbps <= 0 {
		o.HighLayerMinKbps = 1600
	}
	if o.CriticalLayerKbps <= 0 {
		o.CriticalLayerKbps = 120
	}
	if o.MinUpswitchInterval <= 0 {
		o.MinUpswitchInterval = 8 * time.Second
	}
	if o.MinDownswitchInterval <= 0 {
		o.MinDownswitchInterval = 1500 * time.Millisecond
	}
	if o.GoodSamplesForUpgrade <= 0 {
		o.GoodSamplesForUpgrade = 3
	}
	if o.PoorSamplesForDowngrade <= 0 {
		o.PoorSamplesForDowngrade = 1
	}
	if o.EWMAAlpha <= 0 || o.EWMAAlpha > 1 {
		o.EWMAAlpha = 0.35
	}
	return o
}

// NetworkSample is reported by a WebRTC client or future server-side stats
// collector. It represents a single subscriber's network/device health for
// one received video stream.
type NetworkSample struct {
	UserID               string      `json:"user_id,omitempty"`
	StreamID             string      `json:"stream_id"`
	AvailableBitrateKbps int         `json:"available_bitrate_kbps,omitempty"`
	ObservedBitrateKbps  int         `json:"observed_bitrate_kbps,omitempty"`
	PacketLossPct        float64     `json:"packet_loss_pct,omitempty"`
	RoundTripTimeMs      int         `json:"round_trip_time_ms,omitempty"`
	JitterMs             float64     `json:"jitter_ms,omitempty"`
	AudioPacketLossPct   float64     `json:"audio_packet_loss_pct,omitempty"`
	AudioJitterMs        float64     `json:"audio_jitter_ms,omitempty"`
	FramesPerSecond      float64     `json:"frames_per_second,omitempty"`
	DroppedFramesPct     float64     `json:"dropped_frames_pct,omitempty"`
	DecodeTimeMs         float64     `json:"decode_time_ms,omitempty"`
	FreezeCountDelta     int         `json:"freeze_count_delta,omitempty"`
	NACKCountDelta       int         `json:"nack_count_delta,omitempty"`
	PLICountDelta        int         `json:"pli_count_delta,omitempty"`
	DeviceClass          DeviceClass `json:"device_class,omitempty"`
	LowPowerMode         bool        `json:"low_power_mode,omitempty"`
	ScreenShare          bool        `json:"screen_share,omitempty"`
	Timestamp            time.Time   `json:"timestamp,omitempty"`
}

type AdaptiveDecision struct {
	UserID                 string       `json:"user_id,omitempty"`
	StreamID               string       `json:"stream_id"`
	PreviousQuality        QualityLayer `json:"previous_quality"`
	TargetQuality          QualityLayer `json:"target_quality"`
	NetworkGrade           NetworkGrade `json:"network_grade"`
	Changed                bool         `json:"changed"`
	Hold                   bool         `json:"hold"`
	AudioPriority          bool         `json:"audio_priority"`
	VideoSuspended         bool         `json:"video_suspended"`
	SyncMode               string       `json:"sync_mode"`
	VideoDegradeMode       string       `json:"video_degrade_mode"`
	MaxVideoBitrateKbps    int          `json:"max_video_bitrate_kbps"`
	MaxVideoFPS            int          `json:"max_video_fps"`
	TargetAudioBufferMs    int          `json:"target_audio_buffer_ms"`
	TargetVideoBufferMs    int          `json:"target_video_buffer_ms"`
	LipSyncWindowMs        int          `json:"lip_sync_window_ms"`
	EstimatedBandwidthKbps int          `json:"estimated_bandwidth_kbps"`
	StabilityScore         int          `json:"stability_score"`
	Reasons                []string     `json:"reasons"`
	DecidedAt              time.Time    `json:"decided_at"`
}

type AdaptiveController struct {
	mu      sync.Mutex
	options AdaptiveOptions
	state   map[string]*adaptiveState
}

type adaptiveState struct {
	initialized      bool
	bandwidthKbps    float64
	lossPct          float64
	rttMs            float64
	jitterMs         float64
	audioLossPct     float64
	audioJitterMs    float64
	fps              float64
	droppedFramesPct float64
	decodeTimeMs     float64
	lastQuality      QualityLayer
	lastSwitchAt     time.Time
	consecutiveGood  int
	consecutivePoor  int
}

func NewAdaptiveController(options AdaptiveOptions) *AdaptiveController {
	return &AdaptiveController{
		options: options.withDefaults(),
		state:   make(map[string]*adaptiveState),
	}
}

func (c *AdaptiveController) ForgetUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prefix := userID + ":"
	for key := range c.state {
		if strings.HasPrefix(key, prefix) {
			delete(c.state, key)
		}
	}
}

func (c *AdaptiveController) ForgetStream(streamID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	suffix := ":" + streamID
	for key := range c.state {
		if strings.HasSuffix(key, suffix) {
			delete(c.state, key)
		}
	}
}

func (c *AdaptiveController) Decide(sample NetworkSample, current QualityLayer, available []QualityLayer) AdaptiveDecision {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := sample.Timestamp
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if current == "" {
		current = QualityMedium
	}

	key := sample.UserID + ":" + sample.StreamID
	st := c.state[key]
	if st == nil {
		st = &adaptiveState{lastQuality: current, lastSwitchAt: now}
		c.state[key] = st
	}

	c.updateState(st, sample)
	rawQuality, grade, score, audioPriority, suspend, maxBitrate, maxFPS, reasons := c.rawDecision(st, sample)
	target := clampQuality(rawQuality, available)
	target = capForDevice(target, sample.DeviceClass, sample.LowPowerMode, &reasons)
	immediateDownswitch := audioPriority || suspend || isDeviceConstrained(sample.DeviceClass, sample.LowPowerMode)

	if suspend {
		target = clampQuality(QualityLow, available)
	}

	if qualityRank(target) > qualityRank(current) {
		st.consecutiveGood++
		st.consecutivePoor = 0
	} else if qualityRank(target) < qualityRank(current) {
		st.consecutivePoor++
		st.consecutiveGood = 0
	} else {
		st.consecutiveGood = 0
		st.consecutivePoor = 0
	}

	hold := false
	if qualityRank(target) > qualityRank(current) {
		if st.consecutiveGood < c.options.GoodSamplesForUpgrade || now.Sub(st.lastSwitchAt) < c.options.MinUpswitchInterval {
			hold = true
			reasons = append(reasons, "hysteresis: waiting for stable network before upgrading")
			target = current
		}
	} else if qualityRank(target) < qualityRank(current) && grade != NetworkGradeCritical && !immediateDownswitch {
		if st.consecutivePoor < c.options.PoorSamplesForDowngrade || now.Sub(st.lastSwitchAt) < c.options.MinDownswitchInterval {
			hold = true
			reasons = append(reasons, "hysteresis: avoiding short-lived downswitch")
			target = current
		}
	}

	changed := target != current
	if changed {
		st.lastSwitchAt = now
		st.lastQuality = target
	}

	return AdaptiveDecision{
		UserID:                 sample.UserID,
		StreamID:               sample.StreamID,
		PreviousQuality:        current,
		TargetQuality:          target,
		NetworkGrade:           grade,
		Changed:                changed,
		Hold:                   hold,
		AudioPriority:          audioPriority,
		VideoSuspended:         suspend,
		SyncMode:               "audio_clock_master",
		VideoDegradeMode:       videoDegradeMode(target, sample, audioPriority, suspend),
		MaxVideoBitrateKbps:    maxBitrate,
		MaxVideoFPS:            maxFPS,
		TargetAudioBufferMs:    targetAudioBufferMs(grade, audioPriority),
		TargetVideoBufferMs:    targetVideoBufferMs(grade, audioPriority, suspend),
		LipSyncWindowMs:        lipSyncWindowMs(grade, audioPriority),
		EstimatedBandwidthKbps: int(math.Round(st.bandwidthKbps)),
		StabilityScore:         score,
		Reasons:                reasons,
		DecidedAt:              now,
	}
}

func (c *AdaptiveController) updateState(st *adaptiveState, sample NetworkSample) {
	bw := sample.AvailableBitrateKbps
	if bw <= 0 {
		bw = sample.ObservedBitrateKbps
	}
	if !st.initialized {
		st.initialized = true
		st.bandwidthKbps = float64(bw)
		st.lossPct = sample.PacketLossPct
		st.rttMs = float64(sample.RoundTripTimeMs)
		st.jitterMs = sample.JitterMs
		st.audioLossPct = sample.AudioPacketLossPct
		st.audioJitterMs = sample.AudioJitterMs
		st.fps = sample.FramesPerSecond
		st.droppedFramesPct = sample.DroppedFramesPct
		st.decodeTimeMs = sample.DecodeTimeMs
		return
	}
	alpha := c.options.EWMAAlpha
	if bw > 0 {
		st.bandwidthKbps = ewma(st.bandwidthKbps, float64(bw), alpha)
	}
	st.lossPct = ewma(st.lossPct, sample.PacketLossPct, alpha)
	if sample.RoundTripTimeMs > 0 {
		st.rttMs = ewma(st.rttMs, float64(sample.RoundTripTimeMs), alpha)
	}
	st.jitterMs = ewma(st.jitterMs, sample.JitterMs, alpha)
	st.audioLossPct = ewma(st.audioLossPct, sample.AudioPacketLossPct, alpha)
	st.audioJitterMs = ewma(st.audioJitterMs, sample.AudioJitterMs, alpha)
	if sample.FramesPerSecond > 0 {
		st.fps = ewma(st.fps, sample.FramesPerSecond, alpha)
	}
	st.droppedFramesPct = ewma(st.droppedFramesPct, sample.DroppedFramesPct, alpha)
	st.decodeTimeMs = ewma(st.decodeTimeMs, sample.DecodeTimeMs, alpha)
}

func (c *AdaptiveController) rawDecision(st *adaptiveState, sample NetworkSample) (QualityLayer, NetworkGrade, int, bool, bool, int, int, []string) {
	reasons := make([]string, 0, 6)
	score := 100
	score -= int(st.lossPct * 4)
	score -= int(math.Max(0, st.rttMs-120) / 8)
	score -= int(math.Max(0, st.jitterMs-20) / 2)
	score -= int(st.droppedFramesPct * 2)
	score -= int(math.Max(0, st.decodeTimeMs-24) * 2)
	if sample.FreezeCountDelta > 0 {
		score -= 15 + sample.FreezeCountDelta*4
	}
	if sample.PLICountDelta > 0 {
		score -= sample.PLICountDelta * 3
	}
	if sample.NACKCountDelta > 0 {
		score -= minInt(sample.NACKCountDelta, 30)
	}
	if score < 0 {
		score = 0
	}

	grade := gradeFromScore(score)
	audioPriority := st.audioLossPct >= 4 || st.audioJitterMs >= 55
	if audioPriority {
		reasons = append(reasons, "audio priority: preserving speech intelligibility")
	}

	quality := QualityHigh
	bw := int(math.Round(st.bandwidthKbps))
	switch {
	case bw > 0 && bw < c.options.CriticalLayerKbps:
		quality = QualityLow
		grade = NetworkGradeCritical
		reasons = append(reasons, "bandwidth below critical video floor")
	case bw > 0 && bw < c.options.MediumLayerMinKbps:
		quality = QualityLow
		reasons = append(reasons, "bandwidth supports low layer only")
	case bw > 0 && bw < c.options.HighLayerMinKbps:
		quality = QualityMedium
		reasons = append(reasons, "bandwidth supports medium layer")
	default:
		quality = QualityHigh
		reasons = append(reasons, "bandwidth supports high layer")
	}

	if st.lossPct >= 12 || st.rttMs >= 700 || st.jitterMs >= 120 {
		quality = minQuality(quality, QualityLow)
		grade = NetworkGradeCritical
		reasons = append(reasons, "critical transport health")
	} else if st.lossPct >= 5 || st.rttMs >= 400 || st.jitterMs >= 70 {
		quality = minQuality(quality, QualityLow)
		reasons = append(reasons, "poor transport health")
	} else if st.lossPct >= 2 || st.rttMs >= 220 || st.jitterMs >= 40 {
		quality = minQuality(quality, QualityMedium)
		reasons = append(reasons, "moderate transport health")
	}

	if st.droppedFramesPct >= 12 || st.decodeTimeMs >= 45 || sample.FreezeCountDelta >= 2 {
		quality = downgradeQuality(quality)
		reasons = append(reasons, "device decode pressure")
	}

	if audioPriority {
		quality = minQuality(quality, QualityLow)
	}

	suspend := false
	if (bw > 0 && bw < c.options.CriticalLayerKbps) || st.audioLossPct >= 10 || st.audioJitterMs >= 120 {
		suspend = true
		reasons = append(reasons, "video suspend recommended until audio recovers")
	}

	maxBitrate := maxBitrateForQuality(quality, c.options)
	maxFPS := maxFPSForQuality(quality)
	if audioPriority || suspend {
		maxFPS = minInt(maxFPS, 12)
		maxBitrate = minInt(maxBitrate, maxBitrateForQuality(QualityLow, c.options))
	}

	return quality, grade, score, audioPriority, suspend, maxBitrate, maxFPS, reasons
}

func ewma(prev, next, alpha float64) float64 {
	return prev*(1-alpha) + next*alpha
}

func gradeFromScore(score int) NetworkGrade {
	switch {
	case score >= 90:
		return NetworkGradeExcellent
	case score >= 75:
		return NetworkGradeGood
	case score >= 55:
		return NetworkGradeFair
	case score >= 30:
		return NetworkGradePoor
	default:
		return NetworkGradeCritical
	}
}

func clampQuality(q QualityLayer, available []QualityLayer) QualityLayer {
	if len(available) == 0 {
		return q
	}
	allowed := make(map[QualityLayer]bool, len(available))
	for _, layer := range available {
		allowed[layer] = true
	}
	for _, candidate := range layerPriority(q) {
		if allowed[candidate] {
			return candidate
		}
	}
	return available[0]
}

func capForDevice(q QualityLayer, deviceClass DeviceClass, lowPower bool, reasons *[]string) QualityLayer {
	normalized := DeviceClass(strings.ToLower(string(deviceClass)))
	switch {
	case normalized == DeviceClassLegacy:
		*reasons = append(*reasons, "legacy device cap")
		return minQuality(q, QualityLow)
	case normalized == DeviceClassLow || lowPower:
		*reasons = append(*reasons, "low device or power mode cap")
		return minQuality(q, QualityMedium)
	default:
		return q
	}
}

func isDeviceConstrained(deviceClass DeviceClass, lowPower bool) bool {
	normalized := DeviceClass(strings.ToLower(string(deviceClass)))
	return normalized == DeviceClassLegacy || normalized == DeviceClassLow || lowPower
}

func videoDegradeMode(q QualityLayer, sample NetworkSample, audioPriority, suspend bool) string {
	switch {
	case suspend:
		return "suspend_video_until_audio_recovers"
	case audioPriority:
		return "audio_first_resolution_and_fps"
	case sample.ScreenShare:
		return "preserve_resolution_lower_fps"
	case q == QualityLow || q == QualityMedium:
		return "reduce_resolution_first"
	default:
		return "maintain_quality"
	}
}

func targetAudioBufferMs(grade NetworkGrade, audioPriority bool) int {
	if audioPriority {
		return 90
	}
	switch grade {
	case NetworkGradeCritical:
		return 110
	case NetworkGradePoor:
		return 80
	case NetworkGradeFair:
		return 60
	default:
		return 40
	}
}

func targetVideoBufferMs(grade NetworkGrade, audioPriority, suspend bool) int {
	if suspend {
		return 0
	}
	if audioPriority {
		return 150
	}
	switch grade {
	case NetworkGradeCritical:
		return 180
	case NetworkGradePoor:
		return 140
	case NetworkGradeFair:
		return 100
	default:
		return 60
	}
}

func lipSyncWindowMs(grade NetworkGrade, audioPriority bool) int {
	if audioPriority || grade == NetworkGradeCritical {
		return 120
	}
	if grade == NetworkGradePoor {
		return 100
	}
	return 80
}

func qualityRank(q QualityLayer) int {
	switch q {
	case QualityLow:
		return 1
	case QualityMedium:
		return 2
	case QualityHigh:
		return 3
	default:
		return 3
	}
}

func minQuality(a, b QualityLayer) QualityLayer {
	if qualityRank(a) <= qualityRank(b) {
		return a
	}
	return b
}

func downgradeQuality(q QualityLayer) QualityLayer {
	switch q {
	case QualityHigh:
		return QualityMedium
	case QualityMedium:
		return QualityLow
	default:
		return QualityLow
	}
}

func maxBitrateForQuality(q QualityLayer, opts AdaptiveOptions) int {
	switch q {
	case QualityLow:
		return opts.MediumLayerMinKbps - 1
	case QualityMedium:
		return opts.HighLayerMinKbps - 1
	default:
		return opts.HighLayerMinKbps * 2
	}
}

func maxFPSForQuality(q QualityLayer) int {
	switch q {
	case QualityLow:
		return 15
	case QualityMedium:
		return 24
	default:
		return 30
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
