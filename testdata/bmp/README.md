# testdata/bmp

Synthetic BMP streams (RFC 7854 and its updates), one file per behaviour,
named for it. They come from `internal/bmpgen`, are deterministic (fixed
times, no randomness) and hold no data from any collector.

Regenerate them, then read `git diff testdata/bmp`:

    MRT_GOLDEN_UPDATE=1 go test -count=1 ./internal/bmpgen

`TestGeneratedFiles` fails when a file differs from the generator's output,
and checks how many errors each file reads with. `bmp-truncated` ends in a
truncated message. `bmp-garbage` is read with a 1 KiB message limit and has
five errors; peer type 9 and message type 9 in it read without error.
