package proxyclient

import (
	"context"
	"fmt"
	"io"

	"github.com/gorilla/websocket"
)

// Logs streams logs from the given proxy URL
func Logs(ctx context.Context, url string) (io.ReadCloser, error) {
	d := *websocket.DefaultDialer
	d.EnableCompression = true
	conn, res, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	go func() {
		<-ctx.Done()
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ctx.Err().Error()))
	}()

	r, w := io.Pipe()
	cc := &clientConn{
		conn:   conn,
		out:    w,
		errOut: w,
	}
	go func() {
		defer conn.Close()
		err := cc.readMessages(ctx)
		if cErr, ok := err.(*websocket.CloseError); ok && cErr.Code == websocket.CloseNormalClosure {
			err = nil
		}
		w.CloseWithError(err)
	}()
	return r, nil
}

type clientConn struct {
	conn   *websocket.Conn
	out    io.Writer
	errOut io.Writer
}

// commandMessage
type commandMessage struct {
	Operation string `json:"op,omitempty"`
	Data      string `json:"data,omitempty"`
	Width     uint16 `json:"width,omitempty"`
	Height    uint16 `json:"height,omitempty"`
}

func (c *clientConn) readMessages(ctx context.Context) error {
	for ctx.Err() == nil {
		var msg commandMessage
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			return err
		}

		switch msg.Operation {
		case "stdout":
			if c.out != nil {
				_, err := io.WriteString(c.out, msg.Data)
				if err != nil {
					return err
				}
			}
		case "stderr":
			if c.errOut != nil {
				_, err := io.WriteString(c.errOut, msg.Data)
				if err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid operation %s", msg.Operation)
		}
	}

	return ctx.Err()
}

func (c *clientConn) Write(data []byte) (int, error) {
	if err := c.conn.WriteJSON(&commandMessage{
		Operation: "stdin",
		Data:      string(data),
	}); err != nil {
		return 0, err
	}
	return len(data), nil
}
