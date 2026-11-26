# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

摄像头LLM识别配置管理系统 (Camera LLM Recognition System) — polls Hikvision ISC and Yingshi (EZVIZ) cameras on a schedule, captures snapshots, sends them to a vision LLM (via FastGPT) to detect disasters (landslide, bridge collapse, fire, flood), publishes alerts/events over MQTT, and stores results in Postgres. Serves a small server-rendered admin UI (Go `html/template` + static HTML) for configuring cameras and reviewing recognition history. A third device type, `sxgb` (山西省平台国标流 / Shanxi GB28181 platform stream), is display-only: it doesn't capture/run LLM detection, it just periodically resolves and stores a live stream URL for viewing.

Chinese comments and log messages are used throughout; match that style when editing existing files.

## Module layout and the `base` dependency

This module (`video-recognition`) depends on a sibling module `base` via a `replace` directive in `go.mod` (`replace base => ../base`). Both must be checked out side-by-side (as in Jenkins CI, see `jenkinsfile`) for the build to work:

```
gowork/src/
  base/              # shared: base/log, base/utils (mqtt, mail, wechat, qiniu, fastgpt AI calls, ini config, hikvision sig helpers, gin utils)
  video-recognition/ # this module
```

Key `base/utils` helpers used here: `utils.Ai` / `utils.AiAli` (call the FastGPT vision model and parse JSON out of the response), `utils.GIniParser` (reads `conf.ini`), `utils.NewMqttClient`, `utils.SavePicBytesTo7n` (Qiniu upload), `utils.SendMail`, `utils.SendMsgCard` (WeCom).

## Build / run / test

```bash
go mod tidy
go build -o video-recognition main.go
go run main.go
go test ./...
go test ./handler -run TestName -v   # run a single test
```

Local run needs `conf.ini` present in the working directory (DB DSN, MQTT URL, Hikvision/Yingshi/Qiniu/FastGPT credentials — see below). The web server binds `:8080` regardless of the `[web] url` value in `conf.ini` (that key is currently unused by `StartWeb`).

Docker build follows the same two steps inside `Dockerfile`: `go mod tidy` then a static `CGO_ENABLED=0` build; CI (`jenkinsfile`) checks out `base` and `video-recognition` side by side via SVN before invoking `kaniko` on the `Dockerfile`. Deployed via `deploy.yaml` (k8s Deployment `video-recognition` in namespace `iot`; `conf.ini` is mounted from a ConfigMap).

Many tests in `handler/*_test.go` (`recognize_test.go`, `httputil_test.go`) call live external services (Hikvision ISC, live DB, FastGPT) with hardcoded credentials/IPs — they are manual/integration checks, not hermetic unit tests. Expect them to fail without network access to those internal endpoints.

## Configuration (`conf.ini`)

Sections read via `utils.GIniParser`: `[db]` (Postgres DSN for `aicracker`, plus a separate `iota` DSN that appears unused in this module), `[redis]`, `[web]`, `[qiniu]`, `[luan]` (河北路安 upload endpoint), `[ai]` (FastGPT url/key), `[sxgb]` (Shanxi GB28181 stream platform url/username/password/interval_min), plus `[hikisc]`, `[ys]`, `[mqtt]`, `[recognize]`, `[notify]`/`[wx]` sections read directly in `handler` init functions (each with hardcoded defaults if the key is absent — grep `GetStringDefault`/`GetIntDefault` in `handler/` to find them all).

Notable env vars checked at runtime:
- `LLM_MONITOR=true` — required (or `#test` in the camera's custom prompt) for `TaskManager.AddTask` to actually schedule a camera's cron job; otherwise tasks are silently skipped.
- `HIK_ENABLE=false` — disables the Hikvision camera list API entirely.
- `PUSH_ALARM=true` — switches MQTT alarm topic from `ai/alarm-test/{id}` to the real `ai/alarm/{id}`.

## Architecture

**Entrypoint** (`main.go`): initializes `handler.GTaskManager`, loads enabled camera tasks from DB, starts the Gin web server in a goroutine, then blocks on an OS signal channel.

**`db` package**: single `gorm.DB` (`db.GormDb`), auto-migrated on `init()`. Core tables:
- `CameraLLM` — one row per configured camera/task (device type, serial/device IDs, interval, LLM model type, prompt, enabled flag, manual-review flag, project `Group`, optional `YingshiSecret` FK).
- `Recognition` — one row per capture/inference cycle (image path, warning bool, description, audit fields). `PhotoBytes`/`Prompt` are `gorm:"-"` (in-memory only, not persisted).
- `RecognitionAlert` — a denormalized copy written only when an MQTT alarm is actually published (includes topic/payload/publish result for auditing).
- `YingshiSecret` — per-org Yingshi (EZVIZ) AppKey/AppSecret, referenced by `CameraLLM.SecretID`.

**`handler` package** (the bulk of the logic, all in one package):
- `task.go` — `TaskManager` wraps `robfig/cron` (seconds-precision) and maps `CameraLLM.ID -> cron.EntryID`. `AddTask`/`UpdateTask`/`RemoveTask` mutate the live cron schedule to stay in sync with DB changes made through the web handlers. A daily 02:00 job (`checkDeviceNo`) reconciles stale Hikvision serial numbers/device IDs by re-querying the ISC camera list. `performLLMDetection` maps `LLMModelType` (`landslide`/`collapse`/`fire`/`flood`/`custom`) to a Chinese prompt, then calls `Recognition(...)`. `TriggerLocal` is the entry point for externally-supplied detections (see `POST /api/alarm`) — a local model can push its own photo bytes and a `Sure` bool to skip the LLM call entirely.
- `recognize.go` — the actual pipeline: capture (Hik or Yingshi) → download+save image locally under `LocalBaseDirectory/<date>/` → call `utils.Ai`/`utils.AiAli` for the primary disaster-detection prompt → if `warning=true`, run a second LLM pass (`AbnormalPrompt`) to rule out false positives from bad weather/lighting/overexposure → on confirmed warning, upload to Qiniu (`useQiniu`) and to the 河北路安 platform (`uploader.GLuanUploader`, needs `CameraLLM.GatewayId`), send an email/WeCom notification, and (if not `ManualReview`) auto-mark audited; otherwise publish a normal MQTT event. `SendAlarm`/`SendEvent` publish to `ai/alarm/{camera_id}` and `ai/recognize/{camera_id}` respectively and are also invoked manually after human audit (`auditRecognitionHandler` in `web.go`).
- `hik.go` / `httputil.go` — Hikvision ISC Artemis API client: `httputil.go` builds the `x-ca-*` HMAC-signed headers (note the timestamp/nonce are currently hardcoded rather than freshly generated — see `initRequest`); `hik.go` merges two ISC list endpoints (`encodeDevice/search` + `cameras`) into an in-memory `hikCams` map keyed by serial number, used to resolve `GetHikCamInfo`/`GetHikCamFromName`.
- `ys.go` — Yingshi (EZVIZ) open-platform client: fetches/caches access tokens per AppKey (4h TTL) in `YsTokenMaps`, then calls the capture API.
- `sxgb.go` — Shanxi GB28181 stream platform (sxhis.cn) client for `CameraLLM.Type == "sxgb"`: logs in (password encrypted with the platform's SM2/国密2 public key via `github.com/tjfoc/gmsm`, token cached with a 5-minute early-expiry buffer), then calls `getDisasterCameraUrlList` to resolve `SerialNo` (holds the device's SIP code, not a camera serial) → `stream_url`. `RefreshSxgbStreamUrls` runs on its own cron (`[sxgb] interval_min`, default 3) registered directly in `TaskManager.Start()` — independent of `LLM_MONITOR` and of per-camera task registration, since `AddTask` explicitly skips scheduling AI-detection cron jobs for this type (no capture mechanism exists for a live GB28181 stream). The SM2 ciphertext encoding is `C1C3C2` order, standard Base64 (not hex) — confirmed by decoding a real captured login request body; `tjfoc/gmsm`'s `sm2.Encrypt` output already starts with the `04` uncompressed-point byte as part of C1, so don't re-prepend it (see `sm2EncryptBase64`).
- `web.go` — all Gin routes and handlers (`StartWeb`), CRUD for `CameraLLM`, recognition record listing/filtering/CSV export, the audit workflow, and `/api/alarm` for external trigger of `TaskManager.TriggerLocal`. Server-rendered pages (`index.html`, `history.html`, `recognitions.html`, `recognition_detail.html`) are parsed fresh per-request via `html/template.ParseFiles` (no caching), so template edits take effect without a rebuild in dev but every request re-reads the file from disk.

**`uploader` package**: `PhotoUploader` for the 河北路安 (luan) platform — token-based auth with mutex-guarded caching (`ensureAuthenticated`), multipart photo upload keyed by `gatewayID/year/timestamp.ext`.

## Working notes

- `handler` is a single flat package with no sub-packages; when adding camera-platform support, follow the existing `hik.go`/`ys.go` split (one file per platform, a shared `Recognition()` dispatch in `recognize.go` keyed on `camera.Type`).
- Two independent LLM tiers are chosen automatically per camera in `getCameraRecognitionModel`: `LiteModel` (`qwen2.5-vl-7b-instruct`) normally, escalating to `BigModel` (`qwen-vl-max`) once per `bigModelIntervalMinute` (default 60min) per camera — this cadence lives in package-level state (`cameraLastRec` map), not the DB.
- Prompts containing `#test` short-circuit the AI call entirely (the prompt text itself is parsed as if it were the AI's JSON response) — used for exercising the alert/upload pipeline without burning LLM calls; this also affects `TaskManager.AddTask`'s `LLM_MONITOR` gate (see above).
