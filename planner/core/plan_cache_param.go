// Copyright 2022 PingCAP, Inc.
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

package core

import (
	"errors"
	"strings"
	"sync"

	"github.com/pingcap/tidb/parser/ast"
	"github.com/pingcap/tidb/parser/format"
	"github.com/pingcap/tidb/sessionctx"
	driver "github.com/pingcap/tidb/types/parser_driver"
)

var (
	paramReplacerPool = sync.Pool{New: func() interface{} {
		pr := new(paramReplacer)
		pr.Reset()
		return pr
	}}
	paramRestorerPool = sync.Pool{New: func() interface{} {
		pr := new(paramRestorer)
		pr.Reset()
		return pr
	}}
	paramCtxPool = sync.Pool{New: func() interface{} {
		buf := new(strings.Builder)
		buf.Reset()
		restoreCtx := format.NewRestoreCtx(format.DefaultRestoreFlags, buf)
		return restoreCtx
	}}
)

type paramReplacer struct {
	params []*driver.ValueExpr
}

func (pr *paramReplacer) Enter(in ast.Node) (out ast.Node, skipChildren bool) {
	switch n := in.(type) {
	case *driver.ValueExpr:
		pr.params = append(pr.params, n)
		// offset is used as order in general plan cache.
		param := ast.NewParamMarkerExpr(len(pr.params) - 1)
		return param, true
	}
	return in, false
}

func (pr *paramReplacer) Leave(in ast.Node) (out ast.Node, ok bool) {
	return in, true
}

func (pr *paramReplacer) Reset() { pr.params = nil }

// ParameterizeAST parameterizes this StmtNode.
// e.g. `select * from t where a<10 and b<23` --> `select * from t where a<? and b<?`, [10, 23].
// NOTICE: this function may modify the input stmt.
func ParameterizeAST(sctx sessionctx.Context, stmt ast.StmtNode) (paramSQL string, params []*driver.ValueExpr, err error) {
	pr := paramReplacerPool.Get().(*paramReplacer)
	pCtx := paramCtxPool.Get().(*format.RestoreCtx)
	defer func() {
		pr.Reset()
		paramReplacerPool.Put(pr)
		pCtx.In.(*strings.Builder).Reset()
		paramCtxPool.Put(pCtx)
	}()
	stmt.Accept(pr)
	if err := stmt.Restore(pCtx); err != nil {
		err = RestoreASTWithParams(sctx, stmt, pr.params)
		return "", nil, err
	}
	paramSQL, params = pCtx.In.(*strings.Builder).String(), pr.params
	return
}

type paramRestorer struct {
	params []*driver.ValueExpr
	err    error
}

func (pr *paramRestorer) Enter(in ast.Node) (out ast.Node, skipChildren bool) {
	switch n := in.(type) {
	case *driver.ParamMarkerExpr:
		if n.Offset >= len(pr.params) {
			pr.err = errors.New("failed to restore ast.Node")
			return nil, true
		}
		// offset is used as order in general plan cache.
		return pr.params[n.Offset], true
	}
	if pr.err != nil {
		return nil, true
	}
	return in, false
}

func (pr *paramRestorer) Leave(in ast.Node) (out ast.Node, ok bool) {
	return in, true
}

func (pr *paramRestorer) Reset() {
	pr.params, pr.err = nil, nil
}

// RestoreASTWithParams restore this parameterized AST with specific parameters.
// e.g. `select * from t where a<? and b<?`, [10, 23] --> `select * from t where a<10 and b<23`.
func RestoreASTWithParams(_ sessionctx.Context, stmt ast.StmtNode, params []*driver.ValueExpr) error {
	pr := paramRestorerPool.Get().(*paramRestorer)
	defer func() {
		pr.Reset()
		paramRestorerPool.Put(pr)
	}()
	pr.params = params
	stmt.Accept(pr)
	return pr.err
}
