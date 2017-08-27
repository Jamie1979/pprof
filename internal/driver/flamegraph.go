// Copyright © 2017 Martin Spier <spiermar@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package driver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/google/pprof/third_party/d3"
	"github.com/google/pprof/third_party/d3flamegraph"
	"github.com/google/pprof/third_party/d3tip"
)

func init() {
	template.Must(webTemplate.Parse(`
{{define "flame" -}}
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <meta name="viewport" content="width=device-width, initial-scale=1">
       {{template "css" .}}
	<style>
		// d3.flameGraph.css
		{{ .D3FlameGraphCSS }}
	</style>
	<style>
		.flame {
			display: flex;
			flex-direction: column;
			height: 80%;
			min-height: 80%;
			width: 80%;
			min-width: 80%;
			margin-left: 10%;
		}
    </style>
    <title>{{.Title}}</title>
  </head>
  <body>
        {{template "header" .}}
	<div>
		<button id="resetzoom">Reset Zoom</button>
		<div class="flame">
			<div id="errors">{{range .Errors}}<div>{{.}}</div>{{end}}</div>
			<div id="chart"></div>
		</div>
	</div>
	<script type="text/javascript">
		// d3.js
		{{ .D3JS }}
    </script>
	<script type="text/javascript">
		// d3-tip.js
		{{ .D3TipJS }}
    </script>
	<script type="text/javascript">
		// d3.flameGraph.js
		{{ .D3FlameGraphJS }}
    </script>
	<script type="text/javascript">
		var data = {{.Data}};
	</script>
	{{template "script" .}}
	<script type="text/javascript">
		var label = function(d) {
			{{if eq .Unit "nanoseconds"}}
			return d.data.n + " (" + d3.format(".3f")(100 * (d.x1 - d.x0), 3) + "%, " + d3.format(".5f")(d.data.v / 1000000000) + " seconds)";
			{{else}}
			return d.data.n + " (" + d3.format(".3f")(100 * (d.x1 - d.x0), 3) + "%, " + d.data.v + " {{.Unit}})";
			{{end}}
		};

		var width = document.getElementById("chart").clientWidth;

		var flameGraph = d3.flameGraph()
			.width(width)
			.cellHeight(18)
			.minFrameSize(5)
			.transitionDuration(750)
			.transitionEase(d3.easeCubic)
			.sort(true)
			.title("")
			.label(label);

		var tip = d3.tip()
			.direction("s")
			.offset([8, 0])
			.attr('class', 'd3-flame-graph-tip')
			{{if eq .Unit "nanoseconds"}}
			.html(function(d) { return "name: " + d.data.n + ", value: " + d3.format(".5f")(d.data.v / 1000000000) + " seconds"; });
			{{else}}
			.html(function(d) { return "name: " + d.data.n + ", value: " + d.data.v; });
			{{end}}

		flameGraph.tooltip(tip);

		d3.select("#chart")
			.datum(data)
			.call(flameGraph);

		function search(term) {
			if (term == "") {
				flameGraph.clear()
			} else {
				flameGraph.search(term)
			}
		}

		function resetZoom() {
			flameGraph.resetZoom();
		}
		document.getElementById("resetzoom").addEventListener("click", resetZoom);
		
		window.addEventListener("resize", function() {
			var width = document.getElementById("chart").clientWidth;
			var graphs = document.getElementsByClassName("d3-flame-graph");
			if (graphs.length > 0) {
				graphs[0].setAttribute("width", width);
			}
			flameGraph.width(width);
			flameGraph.resetZoom();
		}, true);

		viewer({{.BaseURL}}, null, search)
	</script>
  </body>
</html>
{{end}}`))
}

type flameGraphNode struct {
	Name     string
	Value    int64
	Children map[string]*flameGraphNode
}

func (n *flameGraphNode) add(stackPtr *[]string, index int, value int64) {
	n.Value += value
	if index >= 0 {
		head := (*stackPtr)[index]
		childPtr, ok := n.Children[head]
		if !ok {
			childPtr = &(flameGraphNode{head, 0, make(map[string]*flameGraphNode)})
			n.Children[head] = childPtr
		}
		childPtr.add(stackPtr, index-1, value)
	}
}

func (n *flameGraphNode) MarshalJSON() ([]byte, error) {
	v := make([]flameGraphNode, 0, len(n.Children))
	for _, value := range n.Children {
		v = append(v, *value)
	}

	return json.Marshal(&struct {
		Name     string           `json:"n"`
		Value    int64            `json:"v"`
		Children []flameGraphNode `json:"c"`
	}{
		Name:     n.Name,
		Value:    n.Value,
		Children: v,
	})
}

// flamegraph generates a web page containing a flamegraph.
func (ui *webInterface) flamegraph(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/flamegraph" {
		http.NotFound(w, req)
		return
	}

	// Capture any error messages generated while generating a report.
	catcher := &errorCatcher{UI: ui.options.UI}
	options := *ui.options
	options.UI = catcher

	// Get sample_index from variables
	si := pprofVariables["sample_index"].value

	// Get query parameters
	t := req.URL.Query().Get("t")

	// Defaulting to first SampleType in profile
	index := 0

	if t != "" {
		index, _ = ui.prof.SampleIndexByName(t)
	} else if si != "" {
		index, _ = ui.prof.SampleIndexByName(si)
	}

	rootNode := flameGraphNode{"root", 0, make(map[string]*flameGraphNode)}

	for _, sa := range ui.prof.Sample {
		stack := []string{}
		for _, lo := range sa.Location {
			for _, li := range lo.Line {
				stack = append(stack, li.Function.Name)
			}
		}
		value := sa.Value[index]
		rootNode.add(&stack, len(stack)-1, value)
	}

	b, err := rootNode.MarshalJSON()
	if err != nil {
		http.Error(w, "error serializing flame graph", http.StatusInternalServerError)
		ui.options.UI.PrintErr(err)
		return
	}

	// Looking for profile metadata
	const layout = "Jan 2, 2006 at 3:04pm (MST)"
	file := "unknown"
	if ui.prof.Mapping[0].File != "" {
		file = filepath.Base(ui.prof.Mapping[0].File)
	}
	profileType := ui.prof.SampleType[index].Type
	profileUnit := ui.prof.SampleType[index].Unit

	profileTime := time.Unix(0, ui.prof.TimeNanos).Format(layout)
	profileDuration := fmt.Sprintf("%d ns", ui.prof.DurationNanos)
	if ui.prof.DurationNanos > 1000000000 {
		profileDuration = fmt.Sprintf("%f s", float64(ui.prof.DurationNanos)/1000000000)
	}

	// Creating list of profile types
	profileTypes := []string{}
	for _, s := range ui.prof.SampleType {
		profileTypes = append(profileTypes, s.Type)
	}

	legendUnit := profileUnit
	if legendUnit == "nanoseconds" {
		legendUnit = "seconds"
	}

	legend := []string{
		"File: " + file,
		"Type: " + profileType,
		"Unit: " + legendUnit,
		"Time: " + profileTime,
		"Duration: " + profileDuration,
	}

	// Embed in html.
	data := struct {
		Title           string
		BaseURL         string
		Legend          []string
		Unit            string
		SampleType      string
		SampleTypes     []string
		Errors          []string
		Data            template.JS
		D3JS            template.JS
		D3TipJS         template.JS
		D3FlameGraphJS  template.JS
		D3FlameGraphCSS template.CSS
		Help            map[string]string
	}{
		Title:           file,
		BaseURL:         "/flamegraph",
		Legend:          legend,
		Unit:            profileUnit,
		SampleType:      profileType,
		SampleTypes:     profileTypes,
		Errors:          catcher.errors,
		D3JS:            template.JS(d3.D3JS),
		D3TipJS:         template.JS(d3tip.D3TipJS),
		D3FlameGraphJS:  template.JS(d3flamegraph.D3FlameGraphJS),
		D3FlameGraphCSS: template.CSS(d3flamegraph.D3FlameGraphCSS),
		Data:            template.JS(b),
		Help:            ui.help,
	}
	html := &bytes.Buffer{}
	if err := webTemplate.ExecuteTemplate(html, "flame", data); err != nil {
		http.Error(w, "internal template error", http.StatusInternalServerError)
		ui.options.UI.PrintErr(err)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(html.Bytes())
}
