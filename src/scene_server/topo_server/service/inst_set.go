/*
 * Tencent is pleased to support the open source community by making 蓝鲸 available.
 * Copyright (C) 2017-2018 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the "License"); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 * http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied. See the License for the specific language governing permissions and
 * limitations under the License.
 */

package service

import (
	"strconv"
	"strings"

	"configcenter/src/common"
	"configcenter/src/common/blog"
	"configcenter/src/common/errors"
	"configcenter/src/common/http/rest"
	"configcenter/src/common/mapstr"
	"configcenter/src/common/metadata"
	parser "configcenter/src/common/paraparse"
	"configcenter/src/common/util"
	"configcenter/src/scene_server/topo_server/core/operation"
)

// BatchCreateSet batch create set
func (s *Service) BatchCreateSet(ctx *rest.Contexts) {
	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("parse app_id from url failed, err: %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	type BatchCreateSetRequest struct {
		// shared fields
		BkSupplierAccount string                   `json:"bk_supplier_account"`
		Sets              []map[string]interface{} `json:"sets"`
	}
	batchBody := BatchCreateSetRequest{}
	if err := ctx.DecodeInto(&batchBody); err != nil {
		ctx.RespAutoError(err)
		return
	}

	obj, err := s.Core.ObjectOperation().FindSingleObject(ctx.Kit, common.BKInnerObjIDSet)
	if nil != err {
		blog.Errorf("batch create set failed, get set model failed, err: %s, rid: %s", err.Error(), ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	type OneSetCreateResult struct {
		Index    int         `json:"index"`
		Data     interface{} `json:"data"`
		ErrorMsg string      `json:"error_message"`
	}
	batchCreateResult := make([]OneSetCreateResult, 0)
	var firstErr error
	for idx, set := range batchBody.Sets {
		if _, ok := set[common.BkSupplierAccount]; ok == false {
			set[common.BkSupplierAccount] = batchBody.BkSupplierAccount
		}
		set[common.BKAppIDField] = bizID

		var result interface{}
		// to avoid judging to be nested transaction, need a new header
		ctx.Kit.Header = ctx.Kit.NewHeader()
		txnErr := s.Engine.CoreAPI.CoreService().Txn().AutoRunTxn(ctx.Kit.Ctx, ctx.Kit.Header, func() error {
			var err error
			result, err = s.Core.SetOperation().CreateSet(ctx.Kit, obj, bizID, set)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err != nil && blog.V(3) {
				blog.Errorf("batch create set at index: %d failed, data: %s, err: %v, rid: %s",
					idx, set, err, ctx.Kit.Rid)
			}
			return err
		})

		errMsg := ""
		if txnErr != nil {
			errMsg = txnErr.Error()
		}
		batchCreateResult = append(batchCreateResult, OneSetCreateResult{
			Index:    idx,
			Data:     result,
			ErrorMsg: errMsg,
		})
	}
	ctx.RespEntityWithError(batchCreateResult, firstErr)
}

// CreateSet create a new set
func (s *Service) CreateSet(ctx *rest.Contexts) {
	data := mapstr.MapStr{}
	if err := ctx.DecodeInto(&data); err != nil {
		ctx.RespAutoError(err)
		return
	}

	obj, err := s.Core.ObjectOperation().FindSingleObject(ctx.Kit, common.BKInnerObjIDSet)
	if nil != err {
		blog.Errorf("failed to search the set, %s, rid: %s", err.Error(), ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the biz id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	var resp interface{}
	txnErr := s.Engine.CoreAPI.CoreService().Txn().AutoRunTxn(ctx.Kit.Ctx, ctx.Kit.Header, func() error {
		var err error
		resp, err = s.Core.SetOperation().CreateSet(ctx.Kit, obj, bizID, data)
		if err != nil {
			return err
		}
		return nil
	})

	if txnErr != nil {
		ctx.RespAutoError(txnErr)
		return
	}
	ctx.RespEntity(resp)
}

// CheckIsBuiltInSet check is builtIn set
func (s *Service) CheckIsBuiltInSet(kit *rest.Kit, setIDs ...int64) errors.CCErrorCoder {
	// 检查是否时内置集群
	filter := &metadata.QueryCondition{
		Page: metadata.BasePage{
			Limit: common.BKNoLimit,
		},
		Condition: map[string]interface{}{
			common.BKSetIDField: map[string]interface{}{
				common.BKDBIN: setIDs,
			},
			// 当default值不等于0时为内置集群
			common.BKDefaultField: map[string]interface{}{
				common.BKDBNE: common.DefaultFlagDefaultValue,
			},
		},
	}

	rsp, err := s.Engine.CoreAPI.CoreService().Instance().ReadInstance(kit.Ctx, kit.Header, common.BKInnerObjIDSet,
		filter)
	if nil != err {
		blog.ErrorJSON("read instance failed, option: %s, err: %s, rid: %s", filter, err, kit.Rid)
		return kit.CCError.CCError(common.CCErrCommHTTPDoRequestFailed)
	}
	if rsp.Result == false || rsp.Code != 0 {
		blog.ErrorJSON("read instance failed, ReadInstance failed, option: %s, response: %s, rid: %s",
			filter, rsp, kit.Rid)
		return errors.New(rsp.Code, rsp.ErrMsg)
	}
	if rsp.Data.Count > 0 {
		return kit.CCError.CCError(common.CCErrorTopoForbiddenDeleteOrUpdateBuiltInSetModule)
	}
	return nil
}

// DeleteSets delete sets
func (s *Service) DeleteSets(ctx *rest.Contexts) {
	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the biz id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	data := struct {
		operation.OpCondition `json:",inline"`
	}{}
	if err = ctx.DecodeInto(&data); nil != err {
		blog.Errorf("failed to parse to the operation condition, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.New(common.CCErrCommParamsIsInvalid, err.Error()))
		return
	}

	setIDs := data.Delete.InstID
	// 检查是否时内置集群
	if err := s.CheckIsBuiltInSet(ctx.Kit, setIDs...); err != nil {
		ctx.RespAutoError(err)
		return
	}

	txnErr := s.Engine.CoreAPI.CoreService().Txn().AutoRunTxn(ctx.Kit.Ctx, ctx.Kit.Header, func() error {
		err = s.Core.SetOperation().DeleteSet(ctx.Kit, bizID, data.Delete.InstID)
		if err != nil {
			return err

		}
		return nil
	})

	if txnErr != nil {
		ctx.RespAutoError(txnErr)
		return
	}
	ctx.RespEntity(nil)
}

// DeleteSet delete the set
func (s *Service) DeleteSet(ctx *rest.Contexts) {
	if "batch" == ctx.Request.PathParameter("set_id") {
		s.DeleteSets(ctx)
		return
	}

	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the biz id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	setID, err := strconv.ParseInt(ctx.Request.PathParameter("set_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the set id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "set id"))
		return
	}

	// 检查是否是内置集群
	if err := s.CheckIsBuiltInSet(ctx.Kit, setID); err != nil {
		ctx.RespAutoError(err)
		return
	}

	txnErr := s.Engine.CoreAPI.CoreService().Txn().AutoRunTxn(ctx.Kit.Ctx, ctx.Kit.Header, func() error {
		err = s.Core.SetOperation().DeleteSet(ctx.Kit, bizID, []int64{setID})
		if err != nil {
			blog.Errorf("delete sets failed, %+v, rid: %s", err, ctx.Kit.Rid)
			return err
		}
		return nil
	})

	if txnErr != nil {
		ctx.RespAutoError(txnErr)
		return
	}
	ctx.RespEntity(nil)
}

// UpdateSet update the set
func (s *Service) UpdateSet(ctx *rest.Contexts) {
	data := mapstr.MapStr{}
	if err := ctx.DecodeInto(&data); err != nil {
		ctx.RespAutoError(err)
		return
	}

	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the biz id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	setID, err := strconv.ParseInt(ctx.Request.PathParameter("set_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the set id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "set id"))
		return
	}

	obj, err := s.Core.ObjectOperation().FindSingleObject(ctx.Kit, common.BKInnerObjIDSet)
	if nil != err {
		blog.Errorf("update set failed,failed to search the set, %+v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	// 不允许修改内置集群
	if err := s.CheckIsBuiltInSet(ctx.Kit, setID); err != nil {
		blog.Errorf("check is builtIn set failed, err: %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	txnErr := s.Engine.CoreAPI.CoreService().Txn().AutoRunTxn(ctx.Kit.Ctx, ctx.Kit.Header, func() error {
		err = s.Core.SetOperation().UpdateSet(ctx.Kit, data, obj, bizID, setID)
		if err != nil {
			blog.Errorf("update set failed, err: %+v, rid: %s", err, ctx.Kit.Rid)
			return err
		}

		return nil
	})

	if txnErr != nil {
		ctx.RespAutoError(txnErr)
		return
	}
	ctx.RespEntity(nil)
}

// SearchSet search the set
func (s *Service) SearchSet(ctx *rest.Contexts) {
	bizID, err := strconv.ParseInt(ctx.Request.PathParameter("app_id"), 10, 64)
	if nil != err {
		blog.Errorf("failed to parse the biz id, error info is %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsNeedInt, "business id"))
		return
	}

	data := struct {
		parser.SearchParams `json:",inline"`
		ModelBizID          int64 `json:"bk_biz_id"`
	}{}
	if err = ctx.DecodeInto(&data); nil != err {
		blog.Errorf("search set failed, decode parameter condition failed, err: %+v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.CCError(common.CCErrCommParamsInvalid))
		return
	}
	paramsCond := data.SearchParams
	if paramsCond.Condition == nil {
		paramsCond.Condition = mapstr.New()
	}

	obj, err := s.Core.ObjectOperation().FindSingleObject(ctx.Kit, common.BKInnerObjIDSet)
	if nil != err {
		blog.Errorf("failed to search the set, err: %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	paramsCond.Condition[common.BKAppIDField] = bizID

	queryCond := &metadata.QueryInput{}
	queryCond.Condition = paramsCond.Condition
	queryCond.Fields = strings.Join(paramsCond.Fields, ",")
	page := metadata.ParsePage(paramsCond.Page)
	queryCond.Start = page.Start
	queryCond.Sort = page.Sort
	queryCond.Limit = page.Limit

	cnt, instItems, err := s.Core.SetOperation().FindSet(ctx.Kit, obj, queryCond)
	if nil != err {
		blog.Errorf("failed to find the objects(%s), error info is %v, rid: %s",
			ctx.Request.PathParameter("obj_id"), err, ctx.Kit.Rid)
		ctx.RespAutoError(err)
		return
	}

	result := mapstr.MapStr{}
	result.Set("count", cnt)
	result.Set("info", instItems)

	ctx.RespEntity(result)
	return

}

// SearchSetBatch search the sets in one biz
func (s *Service) SearchSetBatch(ctx *rest.Contexts) {
	bizIDStr := ctx.Request.PathParameter(common.BKAppIDField)
	bizID, err := strconv.ParseInt(bizIDStr, 10, 64)
	if err != nil {
		ctx.RespAutoError(ctx.Kit.CCError.CCErrorf(common.CCErrCommParamsInvalid, common.BKAppIDField))
		return
	}

	option := metadata.SearchInstBatchOption{}
	if err := ctx.DecodeInto(&option); err != nil {
		ctx.RespAutoError(err)
		return
	}

	rawErr := option.Validate()
	if rawErr.ErrCode != 0 {
		ctx.RespAutoError(rawErr.ToCCError(ctx.Kit.CCError))
		return
	}

	setIDs := util.IntArrayUnique(option.IDs)
	cond := mapstr.MapStr{
		common.BKAppIDField: bizID,
		common.BKSetIDField: mapstr.MapStr{
			common.BKDBIN: setIDs,
		},
	}

	qc := &metadata.QueryCondition{
		Fields: option.Fields,
		Page: metadata.BasePage{
			Limit: common.BKNoLimit,
		},
		Condition: cond,
	}
	instanceResult, err := s.Engine.CoreAPI.CoreService().Instance().ReadInstance(ctx.Kit.Ctx, ctx.Kit.Header,
		common.BKInnerObjIDSet, qc)
	if err != nil {
		blog.Errorf("http request failed, err: %v, rid: %s", err, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.Error(common.CCErrCommHTTPDoRequestFailed))
		return
	}
	if !instanceResult.Result {
		blog.ErrorJSON("read instance failed, filter: %s, response: %s, rid: %s", qc, instanceResult, ctx.Kit.Rid)
		ctx.RespAutoError(ctx.Kit.CCError.New(instanceResult.Code, instanceResult.ErrMsg))
		return
	}
	ctx.RespEntity(instanceResult.Data.Info)
}
