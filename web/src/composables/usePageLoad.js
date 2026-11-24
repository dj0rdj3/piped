import { onMounted, onUnmounted, ref } from 'vue';

// Only the latest request may update a page, including its error/loading state.
// Unmounting invalidates pending requests so they cannot affect another page.
export function usePageLoad(request, applyResult, signedOut, { loadOnMount = true } = {}) {
  const loading = ref(false);
  const error = ref('');
  let requestID = 0;

  async function load() {
    const currentRequest = ++requestID;
    loading.value = true;
    error.value = '';
    try {
      const result = await request();
      if (currentRequest === requestID) {
        applyResult(result);
      }
    } catch (err) {
      if (currentRequest !== requestID) {
        return;
      }
      if (err.status === 401) {
        signedOut();
      } else {
        error.value = err.message;
      }
    } finally {
      if (currentRequest === requestID) {
        loading.value = false;
      }
    }
  }

  if (loadOnMount) {
    onMounted(load);
  }
  onUnmounted(() => {
    requestID++;
  });

  return { load, loading, error };
}
