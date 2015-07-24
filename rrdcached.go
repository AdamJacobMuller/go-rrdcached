package rrdcached

import (
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Rrdcached struct {
	Protocol string
	Socket   string
	Ip       string
	Port     int64
	Conn     net.Conn
}

func ConnectToSocket(socket string) *Rrdcached {
	driver := &Rrdcached{
		Protocol: "unix",
		Socket:   socket,
	}
	driver.connect()
	return driver
}

func ConnectToIP(ip string, port int64) *Rrdcached {
	driver := &Rrdcached{
		Protocol: "tcp",
		Ip:       ip,
		Port:     port,
	}
	driver.connect()
	return driver
}

func (r *Rrdcached) connect() {
	var target string

	if r.Protocol == "unix" {
		target = r.Socket
	} else if r.Protocol == "tcp" {
		target = r.Ip + ":" + strconv.FormatInt(r.Port, 10)
	} else {
		panic(fmt.Sprintf("Protocol %v is not recognized: %+v", r.Protocol, r))
	}

	conn, err := net.Dial(r.Protocol, target)
	if err != nil {
		panic(err)
	}
	r.Conn = conn
}

type Stats struct {
	QueueLength     uint64
	UpdatesReceived uint64
	FlushesReceived uint64
	UpdatesWritten  uint64
	DataSetsWritten uint64
	TreeNodesNumber uint64
	TreeDepth       uint64
	JournalBytes    uint64
	JournalRotate   uint64
}

// ---------------------------------------------
// Reflect:
//   http://stackoverflow.com/questions/6395076/in-golang-using-reflect-how-do-you-set-the-value-of-a-struct-field
//   http://stackoverflow.com/questions/24537525/reflect-value-fieldbyname-causing-panic
// TODO: Should I be using reflect?
//   https://groups.google.com/forum/#!topic/golang-nuts/wfmBXg3xML0
// ---------------------------------------------

func parseStats(data string) *Stats {
	lines := strings.Split(data, "\n")

	desc := strings.Split(lines[0], " ")
	count, _ := strconv.ParseInt(desc[0], 10, 64)

	stats := &Stats{}
	stats_struct := reflect.Indirect(reflect.ValueOf(stats))

	for i := 1; i <= int(count); i++ {
		stat := strings.Split(lines[i], ": ")
		stat_label := stat[0]
		stat_value, _ := strconv.ParseUint(stat[1], 10, 64)

		field := stats_struct.FieldByName(stat_label)
		if field.IsValid() && field.CanSet() {
			field.SetUint(stat_value)
		}
	}

	return stats
}

// -------------------------------------------------------------
// Pattern to read forever:
// http://stackoverflow.com/questions/2886719/unix-sockets-in-go
// -------------------------------------------------------------

func readData(r io.Reader) string {
	data := ""

	for {
		buf := make([]byte, 1024)
		n, err := r.Read(buf[:])
		if err != nil {
			panic(err)
		}
		data += string(buf[0:n])

		// If response starts with a positive number,
		// that indicates how many additional lines are expected.
		// Otherwise, go ahead and break.
		check := strings.Split(data, " ")
		if len(check) > 1 {
			status, err := strconv.ParseUint(check[0], 10, 64)

			// Not a number.
			if err != nil {
				break
			}
			// Not a positive number.
			if status <= 0 {
				break
			}
			// More lines are expected, do we have them all yet?
			lines := strings.Split(data, "\n")
			if uint64(len(lines)) >= (status + 1) {
				break
			}
		}
	}

	return data
}

func writeData(conn net.Conn, data string) {
	fmt.Printf("========== %v", data)

	_, err := conn.Write([]byte(data))
	if err != nil {
		panic(err)
	}
}

type Response struct {
	Status  int
	Message string
	Raw     string
}

func checkResponse(conn net.Conn) *Response {
	data := readData(conn)
	data = strings.TrimSpace(data)
	fmt.Println(data)

	lines := strings.SplitN(data, " ", 2)

	status, _ := strconv.ParseInt(lines[0], 10, 0)

	return &Response{
		Status:  int(status),
		Message: lines[1],
		Raw:     data,
	}
}

func NowString() string {
	// rrdcached doesn't grok milliseconds before v1.4.5:
	// https://lists.oetiker.ch/pipermail/rrd-users/2011-May/017816.html
	precision := 0 // 3 is supported in newer versions
	ms := float64(time.Now().UnixNano()) / float64(time.Second)
	return strconv.FormatFloat(ms, 'f', precision, 64)
}

// ----------------------------------------------------------

func (r *Rrdcached) GetStats() *Stats {
	writeData(r.Conn, "STATS\n")
	data := readData(r.Conn)
	return parseStats(data)
}

func (r *Rrdcached) Create(filename string, start int64, step int64, overwrite bool, ds []string, rra []string) *Response {
	var params []string
	if start >= 0 {
		params = append(params, fmt.Sprintf("-b %d", start))
	}
	if step >= 0 {
		params = append(params, fmt.Sprintf("-s %d", step))
	}
	if !overwrite {
		params = append(params, "-O")
	}
	if ds != nil {
		params = append(params, strings.Join(ds, " "))
	}
	if rra != nil {
		params = append(params, strings.Join(rra, " "))
	}

	writeData(r.Conn, "CREATE "+filename+" "+strings.Join(params, " ")+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Update(filename string, values ...string) *Response {
	writeData(r.Conn, "UPDATE "+filename+" "+strings.Join(values, " ")+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Pending(filename string) *Response {
	writeData(r.Conn, "PENDING "+filename+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Forget(filename string) *Response {
	writeData(r.Conn, "FORGET "+filename+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Flush(filename string) *Response {
	writeData(r.Conn, "FLUSH "+filename+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) FlushAll() *Response {
	writeData(r.Conn, "FLUSHALL\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) First(filename string, rraIndex int) *Response {
	writeData(r.Conn, "FIRST "+filename+" "+string(rraIndex)+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Last(filename string) *Response {
	writeData(r.Conn, "LAST "+filename+"\n")
	return checkResponse(r.Conn)
}

func (r *Rrdcached) Quit() {
	writeData(r.Conn, "QUIT\n")
}
