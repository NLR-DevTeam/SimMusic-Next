Oto v3.3.2, Apache-2.0. This local copy preserves upstream sources and license.

Windows changes:
- Disable system ducking on the stream's own audio session before starting it,
  including after output-device recreation.
- Register the WASAPI render and PCM output mixer OS threads with MMCSS
  (Pro Audio), and release each registration before its thread exits.
- Enumerate the default endpoint away from the render thread; bounded event
  waits also allow device switching to be detected when the old device stops
  delivering sample-ready events.

The application supplies a 200 ms total speaker buffer and decodes ahead into a
bounded PCM ring. No system-wide communications volume preference is changed.
