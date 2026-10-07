package media

import "net"

const readBatchSize = 16

type batchReader interface {
	read(buffers [][]byte, datagrams [][]byte) ([][]byte, error)
}

type legReader struct {
	conn      *net.UDPConn
	batch     batchReader
	buffers   [][]byte
	datagrams [][]byte
}

func newLegReader(conn *net.UDPConn, size int) *legReader {
	batch := newBatchReader(conn, size)
	count := 1
	if batch != nil {
		count = size
	}
	memory := make([]byte, count*maxDatagram)
	buffers := make([][]byte, count)
	for i := range buffers {
		buffers[i] = memory[i*maxDatagram : (i+1)*maxDatagram : (i+1)*maxDatagram]
	}
	return &legReader{
		conn:      conn,
		batch:     batch,
		buffers:   buffers,
		datagrams: make([][]byte, 0, count),
	}
}

func (r *legReader) read() ([][]byte, error) {
	r.datagrams = r.datagrams[:0]
	if r.batch != nil {
		var err error
		r.datagrams, err = r.batch.read(r.buffers, r.datagrams)
		return r.datagrams, err
	}
	n, err := r.conn.Read(r.buffers[0])
	if err != nil {
		return nil, err
	}
	r.datagrams = append(r.datagrams, r.buffers[0][:n])
	return r.datagrams, nil
}
