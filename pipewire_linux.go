//go:build linux

package goscap

/*
#cgo pkg-config: libpipewire-0.3

#include <pipewire/pipewire.h>
#include <spa/param/format-utils.h>
#include <spa/param/video/format-utils.h>
#include <spa/utils/result.h>
#include <pthread.h>
#include <string.h>
#include <stdlib.h>
#include <time.h>
#include <errno.h>
#include <sys/mman.h>
#include <unistd.h>

typedef struct pw_frame {
	uint32_t width;
	uint32_t height;
	uint32_t stride;
	uint32_t format;
	uint32_t size;
	uint8_t *data;
	uint64_t seq;
} pw_frame;

typedef struct pw_capture {
	struct pw_thread_loop *loop;
	struct pw_context *context;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook stream_listener;
	struct spa_video_info_raw info;
	uint32_t width;
	uint32_t height;
	uint32_t stride;
	uint32_t format;
	uint8_t *frame;
	uint32_t frame_size;
	uint64_t seq;
	int error;
	pthread_mutex_t lock;
	pthread_cond_t cond;
} pw_capture;

static void on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	pw_capture *c = (pw_capture *)data;
	(void)old;
	if (state == PW_STREAM_STATE_ERROR) {
		pthread_mutex_lock(&c->lock);
		c->error = 1;
		pthread_cond_broadcast(&c->cond);
		pthread_mutex_unlock(&c->lock);
	}
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	pw_capture *c = (pw_capture *)data;
	if (id != SPA_PARAM_Format || param == NULL) {
		return;
	}
	spa_format_video_raw_parse(param, &c->info);
}

static void on_process(void *data) {
	pw_capture *c = (pw_capture *)data;
	struct pw_buffer *b = pw_stream_dequeue_buffer(c->stream);
	if (b == NULL) {
		return;
	}

	struct spa_buffer *buf = b->buffer;
	if (buf == NULL || buf->n_datas == 0) {
		pw_stream_queue_buffer(c->stream, b);
		return;
	}

	struct spa_data *d = &buf->datas[0];
	struct spa_chunk *chunk = d->chunk;
	uint32_t offset = chunk ? chunk->offset : 0;
	uint32_t size = chunk ? chunk->size : d->maxsize;
	uint32_t stride = chunk && chunk->stride ? chunk->stride : 0;

	uint32_t width = c->info.size.width;
	uint32_t height = c->info.size.height;
	if (width == 0 || height == 0) {
		width = c->width;
		height = c->height;
	}
	if (stride == 0 && width > 0) {
		stride = width * 4;
	}
	if (width == 0 || height == 0 || stride == 0) {
		pw_stream_queue_buffer(c->stream, b);
		return;
	}

	uint32_t bytes = stride * height;
	if (bytes > size) {
		bytes = size;
	}

	uint8_t *src = NULL;
	void *mapped = NULL;
	size_t mapped_size = 0;
	if (d->type == SPA_DATA_MemPtr) {
		src = (uint8_t *)d->data;
	} else if (d->type == SPA_DATA_MemFd && d->fd >= 0) {
		mapped_size = d->maxsize;
		if (mapped_size > 0) {
			mapped = mmap(NULL, mapped_size, PROT_READ, MAP_SHARED, d->fd, d->mapoffset);
			if (mapped != MAP_FAILED) {
				src = (uint8_t *)mapped;
			}
		}
	}
	if (src != NULL) {
		src += offset;
		pthread_mutex_lock(&c->lock);
		if (bytes > c->frame_size) {
			uint8_t *newbuf = (uint8_t *)realloc(c->frame, bytes);
			if (newbuf == NULL) {
				pthread_mutex_unlock(&c->lock);
				pw_stream_queue_buffer(c->stream, b);
				if (mapped != NULL && mapped != MAP_FAILED) {
					munmap(mapped, mapped_size);
				}
				return;
			}
			c->frame = newbuf;
			c->frame_size = bytes;
		}
		memcpy(c->frame, src, bytes);
		c->width = width;
		c->height = height;
		c->stride = stride;
		c->format = c->info.format;
		c->seq++;
		pthread_cond_broadcast(&c->cond);
		pthread_mutex_unlock(&c->lock);
	}
	if (mapped != NULL && mapped != MAP_FAILED) {
		munmap(mapped, mapped_size);
	}

	pw_stream_queue_buffer(c->stream, b);
}

static const struct pw_stream_events stream_events = {
	PW_VERSION_STREAM_EVENTS,
	.state_changed = on_state_changed,
	.param_changed = on_param_changed,
	.process = on_process,
};

static pw_capture *pw_capture_new(int fd, uint32_t node_id) {
	pw_capture *c = (pw_capture *)calloc(1, sizeof(pw_capture));
	if (c == NULL) {
		return NULL;
	}
	pthread_mutex_init(&c->lock, NULL);
	pthread_cond_init(&c->cond, NULL);

	pw_init(NULL, NULL);

	c->loop = pw_thread_loop_new("scap", NULL);
	if (c->loop == NULL) {
		free(c);
		return NULL;
	}

	pw_thread_loop_lock(c->loop);
	c->context = pw_context_new(pw_thread_loop_get_loop(c->loop), NULL, 0);
	if (c->context == NULL) {
		pw_thread_loop_unlock(c->loop);
		pw_thread_loop_destroy(c->loop);
		free(c);
		return NULL;
	}

	c->core = pw_context_connect_fd(c->context, fd, NULL, 0);
	if (c->core == NULL) {
		pw_context_destroy(c->context);
		pw_thread_loop_unlock(c->loop);
		pw_thread_loop_destroy(c->loop);
		free(c);
		return NULL;
	}

	c->stream = pw_stream_new(c->core, "scap", pw_properties_new(
		PW_KEY_MEDIA_TYPE, "Video",
		PW_KEY_MEDIA_CATEGORY, "Capture",
		PW_KEY_MEDIA_ROLE, "Screen",
		NULL));
	if (c->stream == NULL) {
		pw_core_disconnect(c->core);
		pw_context_destroy(c->context);
		pw_thread_loop_unlock(c->loop);
		pw_thread_loop_destroy(c->loop);
		free(c);
		return NULL;
	}

	pw_stream_add_listener(c->stream, &c->stream_listener, &stream_events, c);

	uint8_t buffer[512];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
	const struct spa_pod *params[1];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat,
		SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
		SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
		SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(4,
			SPA_VIDEO_FORMAT_BGRA,
			SPA_VIDEO_FORMAT_BGRx,
			SPA_VIDEO_FORMAT_RGBA,
			SPA_VIDEO_FORMAT_RGBx),
		SPA_FORMAT_VIDEO_size, SPA_POD_Rectangle(&SPA_RECTANGLE(0, 0)),
		SPA_FORMAT_VIDEO_framerate, SPA_POD_Fraction(&SPA_FRACTION(0, 1)));

	int res = pw_stream_connect(c->stream,
		PW_DIRECTION_INPUT,
		node_id,
		PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS,
		params, 1);
	if (res < 0) {
		pw_stream_destroy(c->stream);
		pw_core_disconnect(c->core);
		pw_context_destroy(c->context);
		pw_thread_loop_unlock(c->loop);
		pw_thread_loop_destroy(c->loop);
		free(c);
		return NULL;
	}

	pw_thread_loop_start(c->loop);
	pw_thread_loop_unlock(c->loop);
	return c;
}

static int pw_capture_read(pw_capture *c, uint32_t timeout_ms, uint64_t last_seq, pw_frame *out) {
	if (c == NULL || out == NULL) {
		return -1;
	}
	pthread_mutex_lock(&c->lock);
	if (c->error) {
		pthread_mutex_unlock(&c->lock);
		return -2;
	}

	if (timeout_ms == 0) {
		if (c->seq == 0 || c->seq == last_seq) {
			pthread_mutex_unlock(&c->lock);
			return 1;
		}
	} else {
		struct timespec ts;
		clock_gettime(CLOCK_REALTIME, &ts);
		ts.tv_sec += timeout_ms / 1000;
		ts.tv_nsec += (timeout_ms % 1000) * 1000000;
		if (ts.tv_nsec >= 1000000000) {
			ts.tv_sec += 1;
			ts.tv_nsec -= 1000000000;
		}
		while (c->seq == 0 || c->seq == last_seq) {
			int r = pthread_cond_timedwait(&c->cond, &c->lock, &ts);
			if (r == ETIMEDOUT) {
				pthread_mutex_unlock(&c->lock);
				return 1;
			}
			if (c->error) {
				pthread_mutex_unlock(&c->lock);
				return -2;
			}
		}
	}

	out->width = c->width;
	out->height = c->height;
	out->stride = c->stride;
	out->format = c->format;
	out->size = c->frame_size;
	out->seq = c->seq;
	out->data = NULL;
	if (c->frame_size > 0 && c->frame != NULL) {
		out->data = (uint8_t *)malloc(c->frame_size);
		if (out->data == NULL) {
			pthread_mutex_unlock(&c->lock);
			return -3;
		}
		memcpy(out->data, c->frame, c->frame_size);
	}
	pthread_mutex_unlock(&c->lock);
	return 0;
}

static void pw_capture_free_frame(pw_frame *f) {
	if (f == NULL) {
		return;
	}
	if (f->data != NULL) {
		free(f->data);
		f->data = NULL;
	}
}

static void pw_capture_destroy(pw_capture *c) {
	if (c == NULL) {
		return;
	}
	if (c->loop) {
		pw_thread_loop_stop(c->loop);
	}
	if (c->stream) {
		pw_stream_destroy(c->stream);
	}
	if (c->core) {
		pw_core_disconnect(c->core);
	}
	if (c->context) {
		pw_context_destroy(c->context);
	}
	if (c->loop) {
		pw_thread_loop_destroy(c->loop);
	}
	if (c->frame) {
		free(c->frame);
	}
	pthread_mutex_destroy(&c->lock);
	pthread_cond_destroy(&c->cond);
	free(c);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"
)

type pipewireCapturer struct {
	mu          sync.Mutex
	timeout     time.Duration
	pw          *C.pw_capture
	lastImage   *image.RGBA
	lastSeq     uint64
	sessionPath dbus.ObjectPath
	conn        *dbus.Conn
}

func newPipewireCapturer(opts *Options) (Capturer, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}

	sessionPath, nodeID, fd, err := portalCreateStream(conn, opts.OutputIndex)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	pw := C.pw_capture_new(C.int(fd), C.uint32_t(nodeID))
	if pw == nil {
		_ = closePortalSession(conn, sessionPath)
		_ = conn.Close()
		return nil, errors.New("pipewire init failed")
	}

	return &pipewireCapturer{
		timeout:     opts.Timeout,
		pw:          pw,
		sessionPath: sessionPath,
		conn:        conn,
	}, nil
}

func (c *pipewireCapturer) Capture() (*image.RGBA, error) {
	img, _, err := c.CaptureWithInfo()
	return img, err
}

func (c *pipewireCapturer) CaptureWithInfo() (*image.RGBA, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pw == nil {
		return nil, false, errors.New("pipewire not initialized")
	}

	timeout := c.timeout
	if c.lastImage != nil {
		timeout = 0
	}

	var frame C.pw_frame
	ret := C.pw_capture_read(c.pw, C.uint32_t(timeout.Milliseconds()), C.uint64_t(c.lastSeq), &frame)
	if ret == 1 {
		if c.lastImage != nil {
			return c.lastImage, true, nil
		}
		return nil, false, ErrTimeout
	}
	if ret != 0 {
		return nil, false, fmt.Errorf("pipewire capture error: %d", int(ret))
	}
	defer C.pw_capture_free_frame(&frame)

	buf := C.GoBytes(unsafe.Pointer(frame.data), C.int(frame.size))
	img, err := convertPipewireFrame(&frame, buf)
	if err != nil {
		return nil, false, err
	}

	c.lastSeq = uint64(frame.seq)
	c.lastImage = img
	return img, false, nil
}

func (c *pipewireCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pw != nil {
		C.pw_capture_destroy(c.pw)
		c.pw = nil
	}
	if c.conn != nil {
		_ = closePortalSession(c.conn, c.sessionPath)
		_ = c.conn.Close()
		c.conn = nil
	}
	return nil
}

func convertPipewireFrame(frame *C.pw_frame, buf []byte) (*image.RGBA, error) {
	width := int(frame.width)
	height := int(frame.height)
	stride := int(frame.stride)
	if width <= 0 || height <= 0 || stride <= 0 {
		return nil, errors.New("invalid pipewire frame")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcOff := y * stride
		dstOff := y * img.Stride
		src := buf[srcOff : srcOff+width*4]
		dst := img.Pix[dstOff : dstOff+width*4]
		switch uint32(frame.format) {
		case uint32(C.SPA_VIDEO_FORMAT_BGRA), uint32(C.SPA_VIDEO_FORMAT_BGRx):
			for x := 0; x < width; x++ {
				i := x * 4
				dst[i+0] = src[i+2]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+0]
				dst[i+3] = 0xFF
			}
		case uint32(C.SPA_VIDEO_FORMAT_RGBA), uint32(C.SPA_VIDEO_FORMAT_RGBx):
			for x := 0; x < width; x++ {
				i := x * 4
				dst[i+0] = src[i+0]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+2]
				dst[i+3] = 0xFF
			}
		default:
			return nil, fmt.Errorf("unsupported pipewire format: %d", uint32(frame.format))
		}
	}
	return img, nil
}

func portalCreateStream(conn *dbus.Conn, outputIndex int) (dbus.ObjectPath, uint32, int, error) {
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")

	sessionToken := fmt.Sprintf("scap_session_%d", time.Now().UnixNano())
	requestToken := fmt.Sprintf("scap_req_%d", time.Now().UnixNano())

	createOpts := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(requestToken),
	}
	res, err := portalRequest(conn, obj, "org.freedesktop.portal.ScreenCast", "CreateSession", createOpts)
	if err != nil {
		return "", 0, 0, err
	}
	sessionPathVar, ok := res["session_handle"]
	if !ok {
		return "", 0, 0, errors.New("portal: missing session_handle")
	}
	sessionPath, ok := sessionPathVar.Value().(dbus.ObjectPath)
	if !ok {
		return "", 0, 0, errors.New("portal: invalid session_handle type")
	}

	requestToken = fmt.Sprintf("scap_req_%d", time.Now().UnixNano())
	selectOpts := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // monitor
		"multiple":     dbus.MakeVariant(true),
		"cursor_mode":  dbus.MakeVariant(uint32(2)),
		"handle_token": dbus.MakeVariant(requestToken),
	}
	_, err = portalRequest(conn, obj, "org.freedesktop.portal.ScreenCast", "SelectSources", sessionPath, selectOpts)
	if err != nil {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, err
	}

	requestToken = fmt.Sprintf("scap_req_%d", time.Now().UnixNano())
	startOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(requestToken),
	}
	startRes, err := portalRequest(conn, obj, "org.freedesktop.portal.ScreenCast", "Start", sessionPath, "", startOpts)
	if err != nil {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, err
	}
	streamsVar, ok := startRes["streams"]
	if !ok {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, errors.New("portal: missing streams")
	}
	var streams []struct {
		NodeID uint32
		Props  map[string]dbus.Variant
	}
	if err := dbus.Store([]interface{}{streamsVar.Value()}, &streams); err != nil {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, err
	}
	if len(streams) == 0 {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, errors.New("portal: empty streams")
	}
	idx := 0
	if outputIndex > 0 && outputIndex < len(streams) {
		idx = outputIndex
	}
	nodeID := streams[idx].NodeID

	var fd dbus.UnixFD
	call := obj.Call("org.freedesktop.portal.ScreenCast.OpenPipeWireRemote", 0, sessionPath, map[string]dbus.Variant{})
	if call.Err != nil {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, call.Err
	}
	if err := call.Store(&fd); err != nil {
		_ = closePortalSession(conn, sessionPath)
		return "", 0, 0, err
	}

	return sessionPath, nodeID, int(fd), nil
}

func portalRequest(conn *dbus.Conn, obj dbus.BusObject, iface, method string, args ...interface{}) (map[string]dbus.Variant, error) {
	call := obj.Call(iface+"."+method, 0, args...)
	if call.Err != nil {
		return nil, call.Err
	}
	if len(call.Body) == 0 {
		return nil, errors.New("portal: empty response")
	}
	requestPath, ok := call.Body[0].(dbus.ObjectPath)
	if !ok {
		return nil, errors.New("portal: invalid request path")
	}

	ch := make(chan *dbus.Signal, 1)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)

	rule := fmt.Sprintf("type='signal',sender='org.freedesktop.portal.Desktop',interface='org.freedesktop.portal.Request',member='Response',path='%s'", requestPath)
	_ = conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule).Err
	defer conn.BusObject().Call("org.freedesktop.DBus.RemoveMatch", 0, rule)

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case sig := <-ch:
			if sig == nil || sig.Path != requestPath || sig.Name != "org.freedesktop.portal.Request.Response" {
				continue
			}
			if len(sig.Body) < 2 {
				return nil, errors.New("portal: bad response")
			}
			resp, ok := sig.Body[0].(uint32)
			if !ok {
				return nil, errors.New("portal: invalid response code")
			}
			if resp != 0 {
				return nil, fmt.Errorf("portal: request failed code=%d", resp)
			}
			results, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				return nil, errors.New("portal: invalid response results")
			}
			return results, nil
		case <-timer.C:
			return nil, errors.New("portal: request timeout")
		}
	}
}

func closePortalSession(conn *dbus.Conn, sessionPath dbus.ObjectPath) error {
	if sessionPath == "" {
		return nil
	}
	obj := conn.Object("org.freedesktop.portal.Desktop", sessionPath)
	call := obj.Call("org.freedesktop.portal.Session.Close", 0)
	return call.Err
}
