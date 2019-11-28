package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/dustin/go-humanize"
	"github.com/fsnotify/fsnotify"
	"github.com/labstack/echo/v4"
)

type WebServer struct {
	Echo        *echo.Echo
	path        string
	funcMap     template.FuncMap
	templates   map[string]*template.Template
	assets      http.FileSystem
	OnChangeDir func()
	sync.Mutex

	hasWatch        bool
	isRequireReload bool
}

var watcher *fsnotify.Watcher

func NewWebServer(assets http.FileSystem, path string, OnChangeDir func()) *WebServer {
	if OnChangeDir == nil {
		OnChangeDir = func() {}
	}
	web := &WebServer{
		Echo:        echo.New(),
		path:        path,
		templates:   map[string]*template.Template{},
		assets:      assets,
		OnChangeDir: OnChangeDir,
		funcMap:     template.FuncMap{},
	}

	web.addDefaultTemplateFuncMap()

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		WebPath, err := filepath.Abs(path)
		if err != nil {
			log.Fatalln(err)
		}
		NewFileWatcher(WebPath, func(ev string, path string) {
			if strings.HasPrefix(filepath.Ext(path), ".htm") || strings.HasPrefix(filepath.Ext(path), ".json") {
				web.isRequireReload = true
			}
		})
		web.hasWatch = true
	}
	web.UpdateRender()
	web.Echo.Renderer = web

	return web
}

func (web *WebServer) AddTemplateFuncMap(name string, f func(v interface{}) string) {
	web.funcMap[name] = f
}

func (web *WebServer) addDefaultTemplateFuncMap() {
	web.AddTemplateFuncMap("insertComma", insertComma(0))
	web.AddTemplateFuncMap("insertComma0digit", insertComma(-1))
	web.AddTemplateFuncMap("insertComma3digit", insertComma(3))
	web.AddTemplateFuncMap("marshal", func(v interface{}) string {
		a, _ := json.Marshal(v)
		return string(a)
	})

}

func insertComma(Digit int) func(val interface{}) string {
	return func(val interface{}) string {
		src, ok := val.(string)
		if !ok {
			fval, ok := val.(float64)
			if ok {
				src = fmt.Sprintf("%f", fval)
			} else {
				return fmt.Sprintf("%f", fval)
			}
		}
		if src == "" {
			return src
		}
		strs := strings.Split(src, ".")
		n := new(big.Int)
		n, ok = n.SetString(strs[0], 10)
		if !ok {
			fmt.Println("SetString: error")
			return src
		}
		result := humanize.BigComma(n)

		if len(strs) > 1 {
			if Digit > 0 {
				if len(strs[1]) > Digit {
					strs[1] = strs[1][:Digit]
				}
			}
			if Digit >= 0 {
				result += "." + strs[1]
			}
		}
		return result
	}
}

func (web *WebServer) CheckWatch() {
	if web.isRequireReload {
		web.Lock()
		if web.isRequireReload {
			err := web.UpdateRender()
			if err != nil {
				log.Println(err)
			} else {
				web.isRequireReload = false
			}
		}
		web.Unlock()
	}
}

func (web *WebServer) assetToData(path string) ([]byte, error) {
	f, err := web.assets.Open(path)
	if err != nil {
		return nil, err
	}

	bs, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}

	return bs, nil
}

func (web *WebServer) UpdateRender() error {
	web.OnChangeDir()

	web.templates = map[string]*template.Template{}

	layout, err := web.assets.Open("layout")
	if err != nil {
		log.Fatal(err)
	}
	li, err := layout.Stat()
	if err != nil {
		log.Fatal(err)
	}
	if !li.IsDir() {
		log.Fatal("layout is not folder")
	}

	templateMap := map[string][][]byte{}
	tds := web.loadTemplates("/layout/", "", layout, templateMap)
	templateMap[""] = tds

	moduleTemplateMap := map[string][][]byte{}
	if module, err := web.assets.Open("module"); err == nil {
		li, err := module.Stat()
		if err != nil {
			log.Fatal(err)
		}
		if !li.IsDir() {
			log.Fatal("module is not folder")
		}
		tds := web.loadTemplates("/module/", "", module, moduleTemplateMap)
		moduleTemplateMap[""] = tds
	}

	web.updateRender("", "/view", templateMap, moduleTemplateMap)

	return nil
}

func (web *WebServer) findNearTp(start, prefix, tpName string) []byte {
	var tpData []byte
	var err error
	pfs := strings.Split(strings.Trim(prefix, "/"), "/")
	for i := len(pfs); i >= 0; i-- {
		tpData, err = web.assetToData(start + strings.Join(pfs[:i], "/") + "/" + tpName + ".html")
		if err == nil {
			// log.Println("find", start+strings.Join(pfs[:i], "/")+"/"+tpName+".html")
			return tpData
		}
	}

	return nil
}

func (web *WebServer) loadTemplates(start, prefix string, layout http.File, templateMap map[string][][]byte) [][]byte {
	tds := [][]byte{}

	tpData := web.findNearTp(start, prefix, "layout")
	if tpData != nil {
		tds = append(tds, tpData)
	}
	tpData = web.findNearTp(start, prefix, "base")
	if tpData != nil {
		tds = append(tds, tpData)
	}

	f, err := layout.Readdir(1)
	for err == nil && len(f) > 0 {
		if f[0].IsDir() {
			pf := prefix + f[0].Name() + "/"
			l, err := web.assets.Open(start + pf)
			if err == nil {
				tds := web.loadTemplates(start, pf, l, templateMap)
				templateMap[pf] = tds
			} else {
				log.Println(err)
			}
			f, err = layout.Readdir(1)
			continue
		}
		if f[0].Name() == "layout.html" || f[0].Name() == "base.html" {
			f, err = layout.Readdir(1)
			continue
		}
		data, err := web.assetToData(start + prefix + f[0].Name())
		if err != nil {
			log.Fatal(err)
		}
		tds = append(tds, data)
		f, err = layout.Readdir(1)
	}

	return tds
}

func (web *WebServer) updateRender(prefix, path string, viewTemplateMap map[string][][]byte, moduleTemplateMap map[string][][]byte) error {
	d, err := web.assets.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	var fi []os.FileInfo
	fi, err = d.Readdir(1)
	for err == nil {
		if fi[0].IsDir() {
			web.updateRender(prefix+fi[0].Name()+"/", "/view/"+prefix+fi[0].Name(), viewTemplateMap, moduleTemplateMap)
		} else {
			data, err := web.assetToData(path + "/" + fi[0].Name())
			if err != nil {
				log.Fatal(err)
			}

			t := template.New(fi[0].Name()).
				Delims("<%", "%>").
				Funcs(web.funcMap)

			// template.Must(t.Parse(string(data)))
			t.Parse(string(data))
			paths := strings.Split(strings.Trim(prefix, "/"), "/")
			for i, _ := range paths {
				k := strings.Join(paths[:len(paths)-i], "/")
				if k != "" {
					k += "/"
				}
				if tds, has := viewTemplateMap[k]; has {
					for _, td := range tds {
						t.Parse(string(td))
					}
					break
				}
			}
			for i := 0; i <= len(paths); i++ {
				var k string
				if i == 0 {
					k = ""
				} else {
					k = strings.Join(paths[:i], "/") + "/"
				}
				//log.Println("prefix", prefix+fi[0].Name())
				if tds, has := moduleTemplateMap[k]; has {
					for _, td := range tds {
						t.Parse(string(td))
					}
				}
			}

			web.templates[prefix+fi[0].Name()] = t
			log.Println(prefix + fi[0].Name())
		}

		fi, err = d.Readdir(1)
	}

	return nil

}

func (web *WebServer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	web.CheckWatch()
	tmpl, ok := web.templates[name]
	if !ok {
		err := errors.New("Template not found -> " + name)
		return err
	}
	return tmpl.ExecuteTemplate(w, "base.html", data)
}
