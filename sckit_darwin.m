//go:build darwin
// +build darwin

#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreVideo/CoreVideo.h>
#import <CoreMedia/CoreMedia.h>
#import <Foundation/Foundation.h>
#import <pthread.h>
#import <stdlib.h>
#import <string.h>
#import <time.h>
#import <errno.h>

#include "sckit.h"

typedef struct sc_capture {
	SCStream *stream;
	SCStreamConfiguration *config;
	SCContentFilter *filter;
	dispatch_queue_t queue;
	id handler;
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
} sc_capture;

@interface SCStreamHandler : NSObject <SCStreamOutput>
@property (nonatomic, assign) sc_capture *cap;
@end

@implementation SCStreamHandler
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type {
	(void)stream;
	if (type != SCStreamOutputTypeScreen || sampleBuffer == NULL) {
		return;
	}
	CVPixelBufferRef pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
	if (pixelBuffer == NULL) {
		return;
	}
	CVPixelBufferLockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
	void *base = CVPixelBufferGetBaseAddress(pixelBuffer);
	size_t width = CVPixelBufferGetWidth(pixelBuffer);
	size_t height = CVPixelBufferGetHeight(pixelBuffer);
	size_t stride = CVPixelBufferGetBytesPerRow(pixelBuffer);
	OSType format = CVPixelBufferGetPixelFormatType(pixelBuffer);
	uint32_t bytes = (uint32_t)(stride * height);
	if (base != NULL && bytes > 0) {
		pthread_mutex_lock(&self.cap->lock);
		if (bytes > self.cap->frame_size) {
			uint8_t *newbuf = (uint8_t *)realloc(self.cap->frame, bytes);
			if (newbuf == NULL) {
				pthread_mutex_unlock(&self.cap->lock);
				CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
				return;
			}
			self.cap->frame = newbuf;
			self.cap->frame_size = bytes;
		}
		memcpy(self.cap->frame, base, bytes);
		self.cap->width = (uint32_t)width;
		self.cap->height = (uint32_t)height;
		self.cap->stride = (uint32_t)stride;
		self.cap->format = (uint32_t)format;
		self.cap->seq++;
		pthread_cond_broadcast(&self.cap->cond);
		pthread_mutex_unlock(&self.cap->lock);
	}
	CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
}
@end

static int sc_is_available() {
	Class cls = NSClassFromString(@"SCShareableContent");
	return cls != Nil;
}

sc_capture *sc_capture_new(uint32_t display_index) {
	@autoreleasepool {
		if (!sc_is_available()) {
			return NULL;
		}
		sc_capture *c = (sc_capture *)calloc(1, sizeof(sc_capture));
		if (c == NULL) {
			return NULL;
		}
		pthread_mutex_init(&c->lock, NULL);
		pthread_cond_init(&c->cond, NULL);

		__block SCShareableContent *content = nil;
		__block NSError *contentErr = nil;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		[SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent * _Nullable sc, NSError * _Nullable err) {
			content = sc;
			contentErr = err;
			dispatch_semaphore_signal(sem);
		}];
		dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
		if (content == nil || contentErr != nil) {
			free(c);
			return NULL;
		}

		NSArray<SCDisplay *> *displays = content.displays;
		if (displays.count == 0) {
			free(c);
			return NULL;
		}
		if (display_index >= displays.count) {
			display_index = 0;
		}
		SCDisplay *display = displays[display_index];
		c->filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
		c->config = [SCStreamConfiguration new];
		c->config.width = display.width;
		c->config.height = display.height;
		c->config.pixelFormat = kCVPixelFormatType_32BGRA;
		c->config.showsCursor = YES;
		c->config.queueDepth = 5;

		c->stream = [[SCStream alloc] initWithFilter:c->filter configuration:c->config delegate:nil];
		if (c->stream == nil) {
			free(c);
			return NULL;
		}

		c->queue = dispatch_queue_create("scap.scstream", DISPATCH_QUEUE_SERIAL);
		SCStreamHandler *handler = [SCStreamHandler new];
		handler.cap = c;
		c->handler = handler;
		NSError *addErr = nil;
		[c->stream addStreamOutput:handler type:SCStreamOutputTypeScreen sampleHandlerQueue:c->queue error:&addErr];
		if (addErr != nil) {
			free(c);
			return NULL;
		}

		__block NSError *startErr = nil;
		dispatch_semaphore_t startSem = dispatch_semaphore_create(0);
		[c->stream startCaptureWithCompletionHandler:^(NSError * _Nullable err) {
			startErr = err;
			dispatch_semaphore_signal(startSem);
		}];
		dispatch_semaphore_wait(startSem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
		if (startErr != nil) {
			free(c);
			return NULL;
		}

		return c;
	}
}

int sc_capture_read(sc_capture *c, uint32_t timeout_ms, uint64_t last_seq, sc_frame *out) {
	if (c == NULL || out == NULL) {
		return -1;
	}
	pthread_mutex_lock(&c->lock);
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

void sc_capture_free_frame(sc_frame *f) {
	if (f == NULL) {
		return;
	}
	if (f->data != NULL) {
		free(f->data);
		f->data = NULL;
	}
}

void sc_capture_destroy(sc_capture *c) {
	if (c == NULL) {
		return;
	}
	@autoreleasepool {
		if (c->stream != nil) {
			[c->stream stopCaptureWithCompletionHandler:^(NSError * _Nullable err) { (void)err; }];
			[c->stream removeStreamOutput:c->handler type:SCStreamOutputTypeScreen error:nil];
		}
	}
	if (c->frame) {
		free(c->frame);
	}
	pthread_mutex_destroy(&c->lock);
	pthread_cond_destroy(&c->cond);
	free(c);
}
