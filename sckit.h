#ifndef SCAP_SCKIT_H
#define SCAP_SCKIT_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct sc_frame {
	uint32_t width;
	uint32_t height;
	uint32_t stride;
	uint32_t format;
	uint32_t size;
	uint8_t *data;
	uint64_t seq;
} sc_frame;

typedef struct sc_capture sc_capture;

sc_capture *sc_capture_new(uint32_t display_index);
int sc_capture_read(sc_capture *c, uint32_t timeout_ms, uint64_t last_seq, sc_frame *out);
void sc_capture_free_frame(sc_frame *f);
void sc_capture_destroy(sc_capture *c);

#ifdef __cplusplus
}
#endif

#endif
