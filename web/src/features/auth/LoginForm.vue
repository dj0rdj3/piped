<script setup>
import { onUnmounted, ref } from 'vue';
import { LoaderCircle, Terminal } from '@lucide/vue';
import AppButton from '../../components/ui/AppButton.vue';
import FormInput from '../../components/ui/FormInput.vue';
import StatusMessage from '../../components/ui/StatusMessage.vue';
import { api } from '../../lib/api.js';

defineProps({ notice: { type: String, default: '' } });
const emit = defineEmits(['authenticated']);
const username = ref('');
const password = ref('');
const error = ref('');
const busy = ref(false);
let mounted = true;
onUnmounted(() => {
  mounted = false;
  password.value = '';
});

async function submit() {
  if (busy.value) {
    return;
  }

  busy.value = true;
  error.value = '';
  try {
    const result = await api('/auth/login', {
      method: 'POST',
      body: { username: username.value, password: password.value },
    });
    if (mounted) {
      emit('authenticated', result.user, result.development);
    }
  } catch (err) {
    if (mounted) {
      error.value = err.message;
    }
  } finally {
    password.value = '';
    busy.value = false;
  }
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center px-4 py-8">
    <section
      class="w-full max-w-sm rounded-[.85rem] border border-border bg-surface p-7 shadow-[0_10px_40px_#0000000a] max-[560px]:p-6 dark:shadow-[0_10px_40px_#00000030]"
      aria-labelledby="signin-title"
    >
      <a
        class="flex w-fit cursor-pointer items-center gap-[.7rem] text-[1.8rem] font-[750] tracking-[-.08rem] text-foreground no-underline focus-visible:outline-3 focus-visible:outline-focus focus-visible:outline-offset-4"
        href="/login"
        aria-label="Piped home"
      >
        <span
          class="grid size-[2.3rem] place-items-center rounded-[.6rem] bg-accent text-accent-foreground"
        >
          <Terminal :size="23" />
        </span>
        piped
      </a>
      <h1
        id="signin-title"
        class="mt-6 mb-[1.1rem] text-[1.6rem] font-semibold tracking-[-.04em]"
      >
        Sign in
      </h1>
      <StatusMessage v-if="notice">{{ notice }}</StatusMessage>
      <form @submit.prevent="submit">
        <div class="my-4">
          <label
            for="username"
            class="mb-2 block text-sm font-[550]"
          >
            Username
          </label>
          <FormInput
            id="username"
            v-model="username"
            name="username"
            autocomplete="username"
            autocapitalize="none"
            spellcheck="false"
            required
            maxlength="64"
            autofocus
            :disabled="busy"
          />
        </div>
        <div class="my-4">
          <label
            for="password"
            class="mb-2 block text-sm font-[550]"
          >
            Password
          </label>
          <FormInput
            id="password"
            v-model="password"
            name="password"
            type="password"
            autocomplete="current-password"
            required
            :disabled="busy"
          />
        </div>
        <StatusMessage
          v-if="error"
          variant="error"
        >
          {{ error }}
        </StatusMessage>
        <AppButton
          type="submit"
          variant="primary"
          class="mt-2 w-full py-3"
          :disabled="busy"
        >
          <LoaderCircle
            v-if="busy"
            class="motion-safe:animate-spin"
            :size="18"
          />
          <template v-if="busy">Signing in&hellip;</template>
          <template v-else>Sign in</template>
        </AppButton>
      </form>
    </section>
  </main>
</template>
