package execution

import "sync"

type stderrBuffer struct {
	mu       sync.Mutex
	data     [8192]byte
	position int
	size     int
}

func (b *stderrBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := len(data)
	if len(data) >= len(b.data) {
		copy(b.data[:], data[len(data)-len(b.data):])
		b.position = 0
		b.size = len(b.data)
		return length, nil
	}
	first := min(len(data), len(b.data)-b.position)
	copy(b.data[b.position:], data[:first])
	copy(b.data[:], data[first:])
	b.position = (b.position + len(data)) % len(b.data)
	b.size = min(len(b.data), b.size+len(data))
	return length, nil
}
func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size < len(b.data) {
		return string(b.data[:b.size])
	}
	return string(b.data[b.position:]) + string(b.data[:b.position])
}
