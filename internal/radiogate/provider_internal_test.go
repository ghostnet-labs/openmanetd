package radiogate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLimitedBuffer_truncatesButReportsFullWrites(t *testing.T) {
	b := &limitedBuffer{max: 5}

	n, err := b.Write([]byte("abc"))
	assert.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.False(t, b.truncated)

	n, err = b.Write([]byte("defgh"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n, "a short count would make exec fail with ErrShortWrite")
	assert.True(t, b.truncated)
	assert.Equal(t, "abcde", b.buf.String())

	n, err = b.Write([]byte("ij"))
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "abcde", b.buf.String(), "nothing past the limit is kept")
}
