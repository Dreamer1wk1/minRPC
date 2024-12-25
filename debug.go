package miniRPC

import (
	"fmt"
	"html/template"
	"net/http"
)

const debugText = `<html>
	<body>
	<title>GeeRPC Services</title>
	{{range .}}
	<hr>
	Service {{.Name}}
	<hr>
		<table>
		<th align=center>Method</th><th align=center>Calls</th>
		{{range $name, $mtype := .Method}}
			<tr>
			<td align=left font=fixed>{{$name}}({{$mtype.ArgType}}, {{$mtype.ReplyType}}) error</td>
			<td align=center>{{$mtype.NumCalls}}</td>
			</tr>
		{{end}}
		</table>
	{{end}}
	</body>
	</html>`

// Must 会在模板解析失败时立即抛出错误
var debug = template.Must(template.New("RPC debug").Parse(debugText))

type debugHTTP struct {
	*Server
}

type debugService struct {
	Name   string
	Method map[string]*methodType // 键为方法名，值为 *methodType
}

// Runs at /debug/miniRPC
func (server debugHTTP) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Build a sorted version of the data.
	var services []debugService
	// server.serviceMap 存储服务名到服务实例的映射
	server.serviceMap.Range(func(namei, svci interface{}) bool {
		// 回调函数遍历 map 转化为 debugService 并添加到 services 列表中
		svc := svci.(*service)
		services = append(services, debugService{
			Name:   namei.(string),
			Method: svc.method,
		})
		return true // 返回 true 继续遍历
	})
	err := debug.Execute(w, services) // 将 services 数据渲染到模板中，生成 HTML 响应
	if err != nil {
		_, _ = fmt.Fprintln(w, "rpc: error executing template:", err.Error())
	}
}
