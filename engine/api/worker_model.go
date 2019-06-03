package api

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/ovh/cds/engine/api/cache"
	"github.com/ovh/cds/engine/api/group"
	"github.com/ovh/cds/engine/api/pipeline"
	"github.com/ovh/cds/engine/api/project"
	"github.com/ovh/cds/engine/api/workermodel"
	"github.com/ovh/cds/engine/service"
	"github.com/ovh/cds/sdk"
)

func (api *API) postWorkerModelHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		// parse request and check data validity
		var data sdk.Model
		if err := service.UnmarshalBody(r, &data); err != nil {
			return err
		}
		if err := data.IsValid(); err != nil {
			return err
		}
		if err := data.IsValidType(); err != nil {
			return err
		}

		if !isAdmin(ctx) {
			// provision is allowed only for CDS Admin or by user with a restricted model
			if !data.Restricted {
				data.Provision = 0
			}

			// if current user is not admin and model is not restricted, a pattern should be given
			if !data.Restricted && data.PatternName == "" {
				return sdk.NewErrorFrom(sdk.ErrWorkerModelNoPattern, "missing model pattern name")
			}
		}

		tx, err := api.mustDB().Begin()
		if err != nil {
			return sdk.WrapError(err, "cannot begin transaction")
		}
		defer tx.Rollback() // nolint

		model, err := workermodel.Create(tx, data, getAPIConsumer(ctx))
		if err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return sdk.WrapError(err, "unable to commit transaction")
		}

		key := cache.Key("api:workermodels:*")
		api.Cache.DeleteAll(key)

		// reload complete worker model
		new, err := workermodel.LoadByID(api.mustDB(), model.ID)
		if err != nil {
			return err
		}

		new.Editable = true

		return service.WriteJSON(w, new, http.StatusOK)
	}
}

func (api *API) putWorkerModelHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		vars := mux.Vars(r)

		groupName := vars["permGroupName"]
		modelName := vars["permModelName"]

		g, err := group.LoadGroup(api.mustDB(), groupName)
		if err != nil {
			return err
		}

		old, errLoad := workermodel.LoadByNameAndGroupIDWithClearPassword(api.mustDB(), modelName, g.ID)
		if errLoad != nil {
			return sdk.WrapError(errLoad, "cannot load worker model")
		}

		// parse request and validate given data
		var data sdk.Model
		if err := service.UnmarshalBody(r, &data); err != nil {
			return err
		}
		if err := data.IsValid(); err != nil {
			return err
		}

		if !isAdmin(ctx) {
			// provision is allowed only for CDS Admin or by user with a restricted model
			if !data.Restricted {
				data.Provision = 0
			}

			if err := workermodel.CopyModelTypeData(old, &data); err != nil {
				return err
			}
		}

		if err := data.IsValidType(); err != nil {
			return err
		}

		tx, err := api.mustDB().Begin()
		if err != nil {
			return sdk.WrapError(err, "cannot begin transaction")
		}
		defer tx.Rollback() // nolint

		model, err := workermodel.Update(tx, old, data)
		if err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return sdk.WrapError(err, "unable to commit transaction")
		}

		key := cache.Key("api:workermodels:*")
		api.Cache.DeleteAll(key)

		new, err := workermodel.LoadByID(api.mustDB(), model.ID)
		if err != nil {
			return err
		}

		new.Editable = true

		return service.WriteJSON(w, new, http.StatusOK)
	}
}

func (api *API) deleteWorkerModelHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		vars := mux.Vars(r)

		groupName := vars["permGroupName"]
		modelName := vars["permModelName"]

		g, err := group.LoadGroup(api.mustDB(), groupName)
		if err != nil {
			return err
		}

		m, err := workermodel.LoadByNameAndGroupID(api.mustDB(), modelName, g.ID)
		if err != nil {
			return sdk.WrapError(err, "cannot load worker model")
		}

		tx, err := api.mustDB().Begin()
		if err != nil {
			return sdk.WrapError(err, "cannot start transaction")
		}

		if err := workermodel.Delete(tx, m.ID); err != nil {
			return sdk.WrapError(err, "cannot delete worker model")
		}

		if err := tx.Commit(); err != nil {
			return sdk.WrapError(err, "Cannot commit transaction")
		}

		key := cache.Key("api:workermodels:*")
		api.Cache.DeleteAll(key)

		return nil
	}
}

func (api *API) getWorkerModelHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		vars := mux.Vars(r)

		groupName := vars["permGroupName"]
		modelName := vars["permModelName"]

		g, err := group.LoadGroup(api.mustDB(), groupName)
		if err != nil {
			return err
		}

		// FIXME implements load options for worker model dao
		m, err := workermodel.LoadByNameAndGroupID(api.mustDB(), modelName, g.ID)
		if err != nil {
			return sdk.WrapError(err, "cannot load worker model")
		}

		if isGroupAdmin(ctx, g) || isAdmin(ctx) {
			m.Editable = true
		}

		return service.WriteJSON(w, m, http.StatusOK)
	}
}

func (api *API) getWorkerModelsHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		if err := r.ParseForm(); err != nil {
			return sdk.WrapError(sdk.ErrWrongRequest, "cannot parse form")
		}

		var filter workermodel.LoadFilter

		binary := r.FormValue("binary")
		if binary != "" {
			filter.Binary = binary
		}

		stateString := r.FormValue("state")
		if stateString != "" {
			o := workermodel.StateFilter(stateString)
			if err := o.IsValid(); err != nil {
				return err
			}
			filter.State = o
		}

		models := []sdk.Model{}
		var err error
		if isMaintainer(ctx) || isAdmin(ctx) {
			models, err = workermodel.LoadAll(api.mustDB(), &filter, workermodel.LoadOptions.Default)
		} else {
			groupIDs := append(sdk.GroupsToIDs(getAPIConsumer(ctx).GetGroups()), group.SharedInfraGroup.ID)
			models, err = workermodel.LoadAllByGroupIDs(api.mustDB(), groupIDs, &filter, workermodel.LoadOptions.Default)
		}
		if err != nil {
			return sdk.WrapError(err, "cannot load worker models")
		}

		return service.WriteJSON(w, models, http.StatusOK)
	}
}

func (api *API) getWorkerModelUsageHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		vars := mux.Vars(r)

		groupName := vars["permGroupName"]
		modelName := vars["permModelName"]

		g, err := group.LoadGroup(api.mustDB(), groupName)
		if err != nil {
			return err
		}

		m, err := workermodel.LoadByNameAndGroupID(api.mustDB(), modelName, g.ID)
		if err != nil {
			return sdk.WrapError(err, "cannot load worker model")
		}

		pips := []sdk.Pipeline{}
		if isMaintainer(ctx) || isAdmin(ctx) {
			pips, err = pipeline.LoadByWorkerModel(api.mustDB(), m)
		} else {
			pips, err = pipeline.LoadByWorkerModelAndGroupIDs(api.mustDB(), m,
				append(sdk.GroupsToIDs(getAPIConsumer(ctx).GetGroups()), group.SharedInfraGroup.ID))
		}
		if err != nil {
			return sdk.WrapError(err, "cannot load pipelines linked to worker model")
		}

		return service.WriteJSON(w, pips, http.StatusOK)
	}
}

func (api *API) getWorkerModelsForProjectHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		vars := mux.Vars(r)
		key := vars[permProjectKey]

		proj, err := project.Load(api.mustDB(), api.Cache, key, project.LoadOptions.WithGroups)
		if err != nil {
			return sdk.WrapError(err, "unable to load projet %s", key)
		}

		groupIDs := make([]int64, len(proj.ProjectGroups))
		for i := range proj.ProjectGroups {
			groupIDs[i] = proj.ProjectGroups[i].Group.ID
		}

		models, err := workermodel.LoadAllActiveAndNotDeprecatedForGroupIDs(api.mustDB(), append(groupIDs, group.SharedInfraGroup.ID))
		if err != nil {
			return err
		}

		return service.WriteJSON(w, models, http.StatusOK)
	}
}

func (api *API) getWorkerModelsForGroupHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		groupID, err := requestVarInt(r, "groupID")
		if err != nil {
			return err
		}

		// check that the group exists and user is part of the group
		g, err := group.LoadGroupByID(api.mustDB(), groupID)
		if err != nil {
			return err
		}

		if isGroupMember(ctx, g) || isAdmin(ctx) {
			return err
		}

		wms, err := workermodel.LoadAllActiveAndNotDeprecatedForGroupIDs(api.mustDB(), []int64{g.ID, group.SharedInfraGroup.ID})
		if err != nil {
			return err
		}

		return service.WriteJSON(w, wms, http.StatusOK)
	}
}

func (api *API) getWorkerModelTypesHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		return service.WriteJSON(w, sdk.AvailableWorkerModelType, http.StatusOK)
	}
}

func (api *API) getWorkerModelCommunicationsHandler() service.Handler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		return service.WriteJSON(w, sdk.AvailableWorkerModelCommunication, http.StatusOK)
	}
}
