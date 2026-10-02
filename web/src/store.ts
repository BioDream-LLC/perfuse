import { configureStore, createAsyncThunk, createSlice } from '@reduxjs/toolkit'
import type { PayloadAction } from '@reduxjs/toolkit'
import { api, ApiError } from './api'
import type { AuditEntry, ChannelSummary, Me, User } from './api'

// Errors from the server are stored as a message plus the individual problems,
// so a validation failure can be shown as a list rather than one line of text
// with semicolons in it.
export interface UiError {
  message: string
  problems: string[]
}

/** Turns anything thrown into the shape ErrorBox renders.
 *
 *  Exported because the settings page needs it too, and a second copy would drift: this one
 *  knows to pull the problems array out of an ApiError, which is where a validation failure
 *  puts its detail. A page with its own version would show only the headline. */
export function toUiError(err: unknown): UiError {
  if (err instanceof ApiError) {
    return { message: err.message, problems: err.problems }
  }
  if (err instanceof Error) {
    return { message: err.message, problems: [] }
  }
  return { message: 'something went wrong', problems: [] }
}

// ---------- session ----------

interface SessionState {
  me: Me | null
  /** checking is true until the first /api/me has resolved, so the app does not
   *  flash the login page for someone who is already signed in. */
  checking: boolean
  signingIn: boolean
  error: UiError | null
}

export const checkSession = createAsyncThunk('session/check', async () => api.me())

export const signIn = createAsyncThunk(
  'session/signIn',
  async (creds: { username: string; password: string }, { rejectWithValue }) => {
    try {
      return await api.login(creds.username, creds.password)
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

export const signOut = createAsyncThunk('session/signOut', async () => {
  await api.logout()
})

const sessionSlice = createSlice({
  name: 'session',
  initialState: { me: null, checking: true, signingIn: false, error: null } as SessionState,
  reducers: {
    clearError(state) {
      state.error = null
    },
    /** Used when any request comes back 401, so an expired session drops straight
     *  to the login screen rather than leaving a dead page. */
    expired(state) {
      state.me = null
    },
  },
  extraReducers: (builder) => {
    builder
      .addCase(checkSession.pending, (state) => {
        state.checking = true
      })
      .addCase(checkSession.fulfilled, (state, action: PayloadAction<Me>) => {
        state.me = action.payload
        state.checking = false
      })
      .addCase(checkSession.rejected, (state) => {
        state.me = null
        state.checking = false
      })
      .addCase(signIn.pending, (state) => {
        state.signingIn = true
        state.error = null
      })
      .addCase(signIn.fulfilled, (state, action: PayloadAction<Me>) => {
        state.me = action.payload
        state.signingIn = false
      })
      .addCase(signIn.rejected, (state, action) => {
        state.signingIn = false
        state.error = (action.payload as UiError) ?? { message: 'sign in failed', problems: [] }
      })
      .addCase(signOut.fulfilled, (state) => {
        state.me = null
      })
  },
})

export const { clearError: clearSessionError, expired: sessionExpired } = sessionSlice.actions

// ---------- channels ----------

interface ChannelsState {
  items: ChannelSummary[]
  broken: Record<string, string>
  loading: boolean
  error: UiError | null
  /** saving is separate from loading so the list does not flicker on save. */
  saving: boolean
  saveError: UiError | null
}

export const loadChannels = createAsyncThunk('channels/load', async (_, { rejectWithValue }) => {
  try {
    return await api.listChannels()
  } catch (err) {
    return rejectWithValue(toUiError(err))
  }
})

export const saveChannel = createAsyncThunk(
  'channels/save',
  async (args: { name?: string; yaml: string }, { rejectWithValue, dispatch }) => {
    try {
      const res = args.name
        ? await api.updateChannel(args.name, args.yaml)
        : await api.createChannel(args.yaml)
      await dispatch(loadChannels())
      return res
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

export const removeChannel = createAsyncThunk(
  'channels/remove',
  async (name: string, { rejectWithValue, dispatch }) => {
    try {
      await api.deleteChannel(name)
      await dispatch(loadChannels())
      return name
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

const channelsSlice = createSlice({
  name: 'channels',
  initialState: {
    items: [],
    broken: {},
    loading: false,
    error: null,
    saving: false,
    saveError: null,
  } as ChannelsState,
  reducers: {
    clearSaveError(state) {
      state.saveError = null
    },
  },
  extraReducers: (builder) => {
    builder
      .addCase(loadChannels.pending, (state) => {
        state.loading = true
      })
      .addCase(loadChannels.fulfilled, (state, action) => {
        state.items = action.payload.channels ?? []
        state.broken = action.payload.broken ?? {}
        state.loading = false
        state.error = null
      })
      .addCase(loadChannels.rejected, (state, action) => {
        state.loading = false
        state.error = (action.payload as UiError) ?? { message: 'could not load channels', problems: [] }
      })
      .addCase(saveChannel.pending, (state) => {
        state.saving = true
        state.saveError = null
      })
      .addCase(saveChannel.fulfilled, (state) => {
        state.saving = false
      })
      .addCase(saveChannel.rejected, (state, action) => {
        state.saving = false
        state.saveError = (action.payload as UiError) ?? { message: 'could not save', problems: [] }
      })
      .addCase(removeChannel.rejected, (state, action) => {
        state.error = (action.payload as UiError) ?? { message: 'could not delete', problems: [] }
      })
  },
})

export const { clearSaveError } = channelsSlice.actions

// ---------- users ----------

interface UsersState {
  items: User[]
  loading: boolean
  error: UiError | null
}

export const loadUsers = createAsyncThunk('users/load', async (_, { rejectWithValue }) => {
  try {
    return (await api.listUsers()).users ?? []
  } catch (err) {
    return rejectWithValue(toUiError(err))
  }
})

export const addUser = createAsyncThunk(
  'users/add',
  async (
    args: { username: string; password: string; role: 'viewer' | 'editor' | 'admin' },
    { rejectWithValue, dispatch },
  ) => {
    try {
      const u = await api.createUser(args.username, args.password, args.role)
      await dispatch(loadUsers())
      return u
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

export const patchUser = createAsyncThunk(
  'users/patch',
  async (
    args: { id: number; patch: { role?: 'viewer' | 'editor' | 'admin'; password?: string; disabled?: boolean } },
    { rejectWithValue, dispatch },
  ) => {
    try {
      const u = await api.updateUser(args.id, args.patch)
      await dispatch(loadUsers())
      return u
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

export const removeUser = createAsyncThunk(
  'users/remove',
  async (id: number, { rejectWithValue, dispatch }) => {
    try {
      await api.deleteUser(id)
      await dispatch(loadUsers())
      return id
    } catch (err) {
      return rejectWithValue(toUiError(err))
    }
  },
)

const usersSlice = createSlice({
  name: 'users',
  initialState: { items: [], loading: false, error: null } as UsersState,
  reducers: {
    clearError(state) {
      state.error = null
    },
  },
  extraReducers: (builder) => {
    builder
      .addCase(loadUsers.pending, (state) => {
        state.loading = true
      })
      .addCase(loadUsers.fulfilled, (state, action) => {
        state.items = action.payload
        state.loading = false
        state.error = null
      })
      .addCase(loadUsers.rejected, (state, action) => {
        state.loading = false
        state.error = (action.payload as UiError) ?? { message: 'could not load users', problems: [] }
      })
      .addCase(addUser.rejected, (state, action) => {
        state.error = (action.payload as UiError) ?? { message: 'could not create the user', problems: [] }
      })
      .addCase(patchUser.rejected, (state, action) => {
        state.error = (action.payload as UiError) ?? { message: 'could not change the user', problems: [] }
      })
      .addCase(removeUser.rejected, (state, action) => {
        state.error = (action.payload as UiError) ?? { message: 'could not delete the user', problems: [] }
      })
  },
})

export const { clearError: clearUsersError } = usersSlice.actions

// ---------- audit ----------

interface AuditState {
  entries: AuditEntry[]
  loading: boolean
  error: UiError | null
}

export const loadAudit = createAsyncThunk('audit/load', async (_, { rejectWithValue }) => {
  try {
    return (await api.listAudit()).entries ?? []
  } catch (err) {
    return rejectWithValue(toUiError(err))
  }
})

const auditSlice = createSlice({
  name: 'audit',
  initialState: { entries: [], loading: false, error: null } as AuditState,
  reducers: {},
  extraReducers: (builder) => {
    builder
      .addCase(loadAudit.pending, (state) => {
        state.loading = true
      })
      .addCase(loadAudit.fulfilled, (state, action) => {
        state.entries = action.payload
        state.loading = false
      })
      .addCase(loadAudit.rejected, (state, action) => {
        state.loading = false
        state.error = (action.payload as UiError) ?? { message: 'could not load the audit log', problems: [] }
      })
  },
})

export const store = configureStore({
  reducer: {
    session: sessionSlice.reducer,
    channels: channelsSlice.reducer,
    users: usersSlice.reducer,
    audit: auditSlice.reducer,
  },
})

export type RootState = ReturnType<typeof store.getState>
export type AppDispatch = typeof store.dispatch
