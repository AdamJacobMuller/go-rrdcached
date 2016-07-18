package rrdcached

import (
	"errors"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"io"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Rrdcached struct {
	Protocol string
	Socket   string
	Ip       string
	Port     int64
	Batch    bool
	Conn     net.Conn
	Rrdio    RRDIO
}

func ConnectToSocket(socket string) (*Rrdcached, error) {
	driver := &Rrdcached{
		Protocol: "unix",
		Socket:   socket,
		Rrdio:    &dataTransport{},
		Batch:    false,
	}
	err := driver.connect()
	return driver, err
}

func ConnectToIP(ip string, port int64) (*Rrdcached, error) {
	driver := &Rrdcached{
		Protocol: "tcp",
		Ip:       ip,
		Port:     port,
		Rrdio:    &dataTransport{},
		Batch:    false,
	}
	driver.connect()
	err := driver.connect()
	return driver, err
}

func (r *Rrdcached) connect() error {
	var target string

	if r.Protocol == "unix" {
		target = r.Socket
	} else if r.Protocol == "tcp" {
		target = r.Ip + ":" + strconv.FormatInt(r.Port, 10)
	} else {
		panic(fmt.Sprintf("Protocol %v is not recognized: %+v", r.Protocol, r))
	}

	conn, err := net.Dial(r.Protocol, target)
	r.Conn = conn
	return err
}

type Stats struct {
	QueueLength     uint64
	CreatesReceived uint64
	UpdatesReceived uint64
	FlushesReceived uint64
	UpdatesWritten  uint64
	DataSetsWritten uint64
	TreeNodesNumber uint64
	TreeDepth       uint64
	JournalBytes    uint64
	JournalRotate   uint64
}

// ----------------------------------------------------------

type PanicError struct {
	Err error
}

func (f *PanicError) Error() string {
	return f.Err.Error()
}

type ConnectionError struct {
	Err error
}

func (f *ConnectionError) Error() string {
	return f.Err.Error()
}

type UnknownCommandError struct {
	Err error
}

func (f *UnknownCommandError) Error() string {
	return f.Err.Error()
}

type FileDoesNotExistError struct {
	Err error
}

func (f *FileDoesNotExistError) Error() string {
	return f.Err.Error()
}

type UnrecognizedArgumentError struct {
	Err error
}

func (f *UnrecognizedArgumentError) Error() string {
	return f.Err.Error()
}

func (f *UnrecognizedArgumentError) BadArgument() string {
	re := regexp.MustCompile(`can't parse argument '(.+)'`)
	matches := re.FindStringSubmatch(f.Error())
	if matches != nil {
		return matches[1]
	} else {
		return ""
	}
}

func checkError(err error) error {
	if err != nil {
		switch {
		case strings.HasPrefix(err.Error(), "dial tcp:"), strings.HasPrefix(err.Error(), "dial unix "):
			return &ConnectionError{err}
		case strings.Contains(err.Error(), " broken pipe"):
			return &ConnectionError{err}
		}
		return &PanicError{err}
	}
	return nil
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

type RRDIO interface {
	ReadData(r io.Reader) (string, error)
	WriteData(conn net.Conn, data string) error
}

type dataTransport struct{}

func (rrdio dataTransport) ReadData(r io.Reader) (string, error) {
	data := ""

	if r == nil {
		return "", &ConnectionError{fmt.Errorf("RRDCacheD is not connected, cannot read data.")}
	}

	for {
		buf := make([]byte, 1024)
		n, err := r.Read(buf[:])
		if err != nil {
			return "", checkError(err)
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

	return data, nil
}

func (rrdio dataTransport) WriteData(conn net.Conn, data string) error {
	if conn == nil {
		return &ConnectionError{fmt.Errorf("RRDCacheD is not connected, cannot write data.")}
	}

	_, err := conn.Write([]byte(data))
	return checkError(err)
}

func (r *Rrdcached) read() (string, error) {
	return r.Rrdio.ReadData(r.Conn)
}

func (r *Rrdcached) write(data string) error {
	return r.Rrdio.WriteData(r.Conn, data)
}

type BatchResponse struct {
	Responses []*Response
}

type Response struct {
	Status  int
	Message string
	Raw     string
	Error   error
}

func (r *Rrdcached) checkBatchResponse() (*BatchResponse, error) {
	data, err := r.read()
	if err != nil {
		return nil, err
	}

	data = strings.TrimSpace(data)
	//fmt.Printf("data = %s\n", data)

	lines := strings.Split(data, "\n")
	//fmt.Printf("lines = %s\n", lines)

	parts := strings.SplitN(lines[0], " ", 2)

	errCount, _ := strconv.ParseInt(parts[0], 10, 0)
	//fmt.Printf("errCount = %s\n", errCount)

	responses := BatchResponse{}
	for i := int64(0); i < errCount; i++ {
		response, err := r.parseResponseLine(lines[i+1])
		if err != nil {
			log.WithFields(log.Fields{"line": lines[i+1], "err": err}).Error("Unable to parse response line")
			continue
		}
		responses.Responses = append(responses.Responses, response)
	}

	return &responses, err
}

func (r *Rrdcached) parseResponseLine(line string) (*Response, error) {
	parts := strings.SplitN(line, " ", 2)

	status, _ := strconv.ParseInt(parts[0], 10, 0)

	var err error

	if int(status) == -1 {
		err = errors.New(parts[1])
		switch {
		case strings.HasPrefix(parts[1], "Unknown command"):
			err = &UnknownCommandError{err}
		case strings.HasPrefix(parts[1], "No such file"):
			err = &FileDoesNotExistError{err}
		case strings.Contains(parts[1], "can't parse argument"):
			err = &UnrecognizedArgumentError{err}
		}
	}

	return &Response{
		Status:  int(status),
		Message: parts[1],
		Error:   err,
	}, err
}
func (r *Rrdcached) checkResponse() (*Response, error) {
	data, err := r.read()
	if err != nil {
		return nil, err
	}

	data = strings.TrimSpace(data)

	lines := strings.SplitN(data, " ", 2)

	status, _ := strconv.ParseInt(lines[0], 10, 0)

	if int(status) == -1 {
		err = errors.New(lines[1])
		switch {
		case strings.HasPrefix(lines[1], "Unknown command"):
			err = &UnknownCommandError{err}
		case strings.HasPrefix(lines[1], "No such file"):
			err = &FileDoesNotExistError{err}
		case strings.Contains(lines[1], "can't parse argument"):
			err = &UnrecognizedArgumentError{err}
		}
	}

	return &Response{
		Status:  int(status),
		Message: lines[1],
		Raw:     data,
	}, err
}

func NowString() string {
	// rrdcached doesn't grok milliseconds before v1.4.5:
	// https://lists.oetiker.ch/pipermail/rrd-users/2011-May/017816.html
	precision := 0 // 3 is supported in newer versions
	ms := float64(time.Now().UnixNano()) / float64(time.Second)
	return strconv.FormatFloat(ms, 'f', precision, 64)
}

// ----------------------------------------------------------

func (r *Rrdcached) GetStats() (*Stats, error) {
	writeErr := r.write("STATS\n")
	if writeErr != nil {
		return nil, writeErr
	}

	data, readErr := r.read()
	return parseStats(data), readErr
}

func (r *Rrdcached) CreateFromBatchResponses(batchResponse *BatchResponse, start int64, step int64, overwrite bool, ds []string, rra []string) (*Response, error) {
	for i := 0; i < len(batchResponse.Responses); i++ {
		if strings.HasPrefix(batchResponse.Responses[i].Message, "No such file: ") {
			filename := strings.Replace(batchResponse.Responses[i].Message, "No such file: ", "", 1)
			r.Create(filename, start, step, overwrite, ds, rra)
		} else {
			fmt.Printf("'%s' does not start with '%s'\n", batchResponse.Responses[i].Message, "No such file: ")
		}
	}
	return nil, nil
}

func (r *Rrdcached) Create(filename string, start int64, step int64, overwrite bool, ds []string, rra []string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Create() called while in batch mode")
	}
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

	err := r.write("CREATE " + filename + " " + strings.Join(params, " ") + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) Update(filename string, values ...string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Update() called while in batch mode")
	}
	err := r.write("UPDATE " + filename + " " + strings.Join(values, " ") + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) Pending(filename string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Pending() called while in batch mode")
	}
	err := r.write("PENDING " + filename + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) Forget(filename string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Forget() called while in batch mode")
	}
	err := r.write("FORGET " + filename + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) Flush(filename string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Flush() called while in batch mode")
	}
	err := r.write("FLUSH " + filename + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) FlushAll() (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("FlushAll() called while in batch mode")
	}
	err := r.write("FLUSHALL\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) First(filename string, rraIndex int) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("First() called while in batch mode")
	}
	err := r.write("FIRST " + filename + " " + strconv.Itoa(rraIndex) + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) Last(filename string) (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("Last() called while in batch mode")
	}
	err := r.write("LAST " + filename + "\n")
	if err != nil {
		return nil, err
	}
	return r.checkResponse()
}

func (r *Rrdcached) BatchStart() (*Response, error) {
	if r.Batch == true {
		return nil, errors.New("BatchStart() called while already in batch mode")
	}
	err := r.write("BATCH\n")
	if err != nil {
		return nil, err
	}
	resp, err := r.checkResponse()
	if err == nil {
		r.Batch = true
	}
	return resp, err
}

func (r *Rrdcached) BatchUpdate(filename string, values ...string) (*Response, error) {
	if r.Batch == false {
		return nil, errors.New("BatchUpdate() called while not in batch mode")
	}
	err := r.write("UPDATE " + filename + " " + strings.Join(values, " ") + "\n")
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *Rrdcached) BatchFinalize() (*BatchResponse, error) {
	if r.Batch == false {
		return nil, errors.New("BatchFinalize() called while not in batch mode")
	}
	err := r.write(".\n")
	if err != nil {
		return nil, err
	}
	resp, err := r.checkBatchResponse()
	if err == nil {
		r.Batch = false
	}
	//fmt.Printf("responses = %s\n", resp)
	return resp, err
}

func (r *Rrdcached) Quit() {
	r.write("QUIT\n")
}
