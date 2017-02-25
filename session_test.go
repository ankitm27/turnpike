package turnpike

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestNextRequestId(t *testing.T) {
	Convey("Incrementing session id", t, func() {
		sess := &Session{}
		Convey("Should increment on subsequent calls", func() {
			So(sess.NextRequestID(), ShouldEqual, 1)
			So(sess.NextRequestID(), ShouldEqual, 2)
		})
		Convey("Should roll over upon reaching the max session id size", func() {
			sess.lastRequestID = MAXREQUESTID
			So(sess.NextRequestID(), ShouldEqual, 1)
		})
	})
}
