<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import {
  KeyRound,
  ListChecks,
  LoaderCircle,
  LogOut,
  PanelLeftClose,
  RefreshCw,
  Server,
  Terminal,
  Users,
} from '@lucide/vue';
import LoginForm from './features/auth/LoginForm.vue';
import UsersPage from './features/users/UsersPage.vue';
import CredentialsPage from './features/credentials/CredentialsPage.vue';
import HostsPage from './features/hosts/HostsPage.vue';
import JobsPage from './features/jobs/JobsPage.vue';
import AppButton from './components/ui/AppButton.vue';
import PageHeading from './components/ui/PageHeading.vue';
import StatusMessage from './components/ui/StatusMessage.vue';
import { api } from './lib/api.js';
import {
  canonicalPath,
  createPageNavigation,
  isPageNavigation,
  routeForPath,
  sessionPath,
} from './lib/routing.js';
import { confirmDiscardChanges } from './lib/unsaved-changes.js';

const developmentBuild = __PIPED_DEVELOPMENT__;

const user = ref(null);
const phase = ref('loading');
const error = ref('');
const loginNotice = ref('');
const loggingOut = ref(false);
const sidebarOpen = ref(false);
const development = ref(false);
const credentialAction = ref(null);
const hostAction = ref(null);
const jobAction = ref(null);
const page = ref(routeForPath(window.location.pathname));
const pageTitles = {
  hosts: 'Hosts',
  jobs: 'Jobs',
  users: 'Users',
  credentials: 'Credentials',
  login: 'Sign in',
};
const pageTitle = computed(() => pageTitles[page.value] || 'Page not found');
const navigation = createPageNavigation(window, syncRoute, confirmDiscardChanges);

function syncRoute() {
  const { pathname, search, hash } = window.location;
  const path =
    phase.value === 'ready'
      ? sessionPath(window.location, !!user.value)
      : canonicalPath(pathname) + search + hash;

  if (path !== pathname + search + hash) {
    window.history.replaceState(window.history.state, '', path);
  }
  page.value = routeForPath(window.location.pathname);
  sidebarOpen.value = false;
  credentialAction.value = null;
  hostAction.value = null;
  jobAction.value = null;
  error.value = '';
  document.title = pageTitle.value + ' - Piped';
  navigation.remember();
}

function navigate(event, path) {
  if (!isPageNavigation(event)) {
    return;
  }
  event.preventDefault();
  navigation.push(path);
}

function authenticated(account, developmentEnabled = false) {
  user.value = account;
  development.value = developmentBuild && developmentEnabled;
  phase.value = 'ready';
  loginNotice.value = '';
  syncRoute();
}

function signedOut(message, rememberPage = true) {
  user.value = null;
  development.value = false;
  sidebarOpen.value = false;
  error.value = '';
  loginNotice.value = message;
  phase.value = 'ready';
  if (!rememberPage) {
    window.history.replaceState(window.history.state, '', '/login');
  }
  syncRoute();
}

async function logout() {
  if (loggingOut.value || !confirmDiscardChanges()) {
    return;
  }
  loggingOut.value = true;
  try {
    await api('/auth/logout', { method: 'POST' });
    signedOut('Signed out.', false);
  } catch (err) {
    error.value = err.message;
  } finally {
    loggingOut.value = false;
  }
}

async function checkSession() {
  try {
    await api('/auth/me');
  } catch (err) {
    if (err.status === 401) {
      signedOut('Session expired. Sign in again.');
    }
  }
}

async function initialize() {
  phase.value = 'loading';
  error.value = '';
  try {
    const result = await api('/auth/me');
    authenticated(result.user, result.development);
  } catch (err) {
    if (err.status === 401) {
      signedOut('');
      return;
    }
    error.value = err.message;
    phase.value = 'error';
  }
}

onMounted(() => {
  syncRoute();
  window.addEventListener('popstate', navigation.pop);
  initialize();
});
onUnmounted(() => window.removeEventListener('popstate', navigation.pop));

if (developmentBuild) {
  watch([user, development], async ([account, enabled], _previous, onCleanup) => {
    if (!account || !enabled) {
      return;
    }
    let stopped = false;
    const registration = new AbortController();
    let unregister = () => {};
    onCleanup(() => {
      stopped = true;
      registration.abort();
      unregister();
    });
    try {
      const { registerWebMCP } = await import('./integrations/webmcp.js');
      if (stopped) {
        return;
      }
      const stop = await registerWebMCP(document.modelContext ?? navigator.modelContext, {
        signal: registration.signal,
        request: api,
        navigate: async (path) => {
          if (!navigation.push(path)) {
            throw new Error('Navigation cancelled because the form has unsaved changes.');
          }
          await nextTick();
        },
        openCredential: async (action) => {
          if (!navigation.push('/credentials')) {
            throw new Error('Navigation cancelled because the form has unsaved changes.');
          }
          credentialAction.value = action;
          await nextTick();
        },
        openHost: async (action) => {
          if (!navigation.push('/hosts')) {
            throw new Error('Navigation cancelled because the form has unsaved changes.');
          }
          hostAction.value = action;
          await nextTick();
        },
        openJob: async (action) => {
          if (!navigation.push('/jobs')) {
            throw new Error('Navigation cancelled because the form has unsaved changes.');
          }
          jobAction.value = action;
          await nextTick();
        },
        authError: checkSession,
      });
      if (stopped) {
        stop();
      } else {
        unregister = stop;
      }
    } catch {
      if (!stopped) {
        console.warn('Development WebMCP tools could not be registered.');
      }
    }
  });
}
</script>

<template>
  <main
    v-if="phase === 'loading'"
    class="flex min-h-dvh flex-col items-center justify-center gap-5 p-8 text-center"
    aria-live="polite"
  >
    <LoaderCircle
      class="motion-safe:animate-spin"
      :size="26"
    />
    <p>Loading&hellip;</p>
  </main>
  <main
    v-else-if="phase === 'error'"
    class="flex min-h-dvh flex-col items-center justify-center gap-5 p-8 text-center"
  >
    <Terminal :size="30" />
    <h1 class="text-[1.8rem] font-[550] tracking-[-.045em] max-[780px]:text-[1.7rem]">
      Could not open Piped
    </h1>
    <p
      class="text-base leading-relaxed text-muted"
      role="alert"
    >
      {{ error }}
    </p>
    <AppButton
      variant="primary"
      @click="initialize"
    >
      <RefreshCw :size="17" />
      Retry
    </AppButton>
  </main>
  <LoginForm
    v-else-if="!user"
    :notice="loginNotice"
    @authenticated="authenticated"
  />
  <div
    v-else
    class="flex min-h-dvh"
  >
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-[19] bg-black/70 dark:bg-black/80"
      @click="sidebarOpen = false"
    ></div>
    <aside
      class="fixed inset-y-0 left-0 z-20 flex w-56 shrink-0 flex-col border-r border-border bg-surface max-[780px]:w-64 max-[780px]:transition-transform max-[780px]:duration-200 motion-reduce:transition-none"
      :class="
        sidebarOpen
          ? 'visible translate-x-0'
          : 'max-[780px]:invisible max-[780px]:-translate-x-full'
      "
    >
      <div class="flex items-center justify-between px-[1.2rem] py-[1.2rem]">
        <a
          class="flex w-fit items-center gap-[.7rem] text-[1.8rem] font-[750] tracking-[-.08rem] text-foreground no-underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          href="/jobs"
          aria-label="Piped home"
          @click="navigate($event, '/jobs')"
        >
          <span
            class="grid size-[2.3rem] place-items-center rounded-[.6rem] bg-accent text-accent-foreground"
          >
            <Terminal :size="22" />
          </span>
          piped
        </a>
        <AppButton
          variant="icon"
          class="min-[781px]:hidden"
          aria-label="Close navigation"
          @click="sidebarOpen = false"
        >
          <PanelLeftClose :size="20" />
        </AppButton>
      </div>
      <nav
        id="main-navigation"
        class="px-3"
        aria-label="Main navigation"
      >
        <a
          class="flex w-full items-center gap-[.7rem] rounded-lg border-0 p-3 text-left text-sm no-underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          href="/jobs"
          :class="
            page === 'jobs'
              ? 'bg-surface-muted font-[550] text-foreground [&>svg]:text-accent-text'
              : 'text-muted hover:bg-surface-hover'
          "
          :aria-current="page === 'jobs' ? 'page' : undefined"
          @click="navigate($event, '/jobs')"
        >
          <ListChecks :size="19" />
          Jobs
        </a>
        <a
          class="flex w-full items-center gap-[.7rem] rounded-lg border-0 p-3 text-left text-sm no-underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          href="/hosts"
          :class="
            page === 'hosts'
              ? 'bg-surface-muted font-[550] text-foreground [&>svg]:text-accent-text'
              : 'text-muted hover:bg-surface-hover'
          "
          :aria-current="page === 'hosts' ? 'page' : undefined"
          @click="navigate($event, '/hosts')"
        >
          <Server :size="19" />
          Hosts
        </a>
        <a
          class="flex w-full items-center gap-[.7rem] rounded-lg border-0 p-3 text-left text-sm no-underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          href="/credentials"
          :class="
            page === 'credentials'
              ? 'bg-surface-muted font-[550] text-foreground [&>svg]:text-accent-text'
              : 'text-muted hover:bg-surface-hover'
          "
          :aria-current="page === 'credentials' ? 'page' : undefined"
          @click="navigate($event, '/credentials')"
        >
          <KeyRound :size="19" />
          Credentials
        </a>
        <a
          class="flex w-full items-center gap-[.7rem] rounded-lg border-0 p-3 text-left text-sm no-underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          href="/users"
          :class="
            page === 'users'
              ? 'bg-surface-muted font-[550] text-foreground [&>svg]:text-accent-text'
              : 'text-muted hover:bg-surface-hover'
          "
          :aria-current="page === 'users' ? 'page' : undefined"
          @click="navigate($event, '/users')"
        >
          <Users :size="19" />
          Users
        </a>
      </nav>
      <div class="mt-auto">
        <div class="flex items-center gap-2.5 border-t border-border px-4 py-3.5">
          <span
            class="grid size-[2.1rem] shrink-0 place-items-center rounded-full border border-border bg-surface-muted text-xs font-[550] text-muted"
          >
            {{ user.username.slice(0, 2).toUpperCase() }}
          </span>
          <div class="min-w-0 flex-1">
            <strong class="block truncate text-sm font-[550]">{{ user.username }}</strong>
          </div>
          <AppButton
            variant="icon"
            aria-label="Sign out"
            title="Sign out"
            :disabled="loggingOut"
            @click="logout"
          >
            <LoaderCircle
              v-if="loggingOut"
              class="motion-safe:animate-spin"
              :size="18"
            />
            <LogOut
              v-else
              :size="18"
            />
          </AppButton>
        </div>
      </div>
    </aside>
    <div class="ml-56 flex min-w-0 flex-1 flex-col max-[780px]:ml-0">
      <StatusMessage
        v-if="error"
        variant="error"
        class="mx-6"
      >
        {{ error }}
      </StatusMessage>
      <UsersPage
        v-if="page === 'users'"
        :current-user="user"
        @open-navigation="sidebarOpen = true"
        @signed-out="signedOut"
        @auth-error="checkSession"
      />
      <CredentialsPage
        v-else-if="page === 'credentials'"
        :requested-action="credentialAction"
        @open-navigation="sidebarOpen = true"
        @signed-out="signedOut('Session expired. Sign in again.')"
        @auth-error="checkSession"
      />
      <HostsPage
        v-else-if="page === 'hosts'"
        :requested-action="hostAction"
        @open-navigation="sidebarOpen = true"
        @signed-out="signedOut('Session expired. Sign in again.')"
        @auth-error="checkSession"
      />
      <JobsPage
        v-else-if="page === 'jobs'"
        :current-user-id="user.id"
        :requested-action="jobAction"
        @open-navigation="sidebarOpen = true"
        @signed-out="signedOut('Session expired. Sign in again.')"
        @auth-error="checkSession"
      />
      <main
        v-else
        class="mx-auto w-full max-w-[86rem] p-6 max-[780px]:p-[1.1rem]"
      >
        <PageHeading
          title="Page not found"
          title-id="not-found-title"
          @open-navigation="sidebarOpen = true"
        />
        <a
          href="/jobs"
          class="inline-flex items-center gap-1.5 border-0 bg-transparent p-0 text-sm text-muted hover:underline cursor-pointer focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
          @click="navigate($event, '/jobs')"
        >
          Go to Jobs
        </a>
      </main>
    </div>
  </div>
</template>
