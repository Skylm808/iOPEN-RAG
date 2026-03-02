import { computed } from 'vue';
import { useWebSocket } from '@vueuse/core';

export const useChatStore = defineStore(SetupStoreId.Chat, () => {
  const conversationId = ref<string>('');
  const input = ref<Api.Chat.Input>({ message: '' });

  const list = ref<Api.Chat.Message[]>([]);

  const authStore = useAuthStore();

  // 用 computed 让 WebSocket URL 随 token 变化而响应式更新。
  // immediate: false —— 不在 store 初始化时立即连接，避免使用 localStorage 中的旧 token 建立连接。
  // 登录成功后由 auth store 调用 reconnect() 主动用新 token 重建连接。
  const wsUrl = computed(() => `/proxy-ws/chat/${authStore.token}`);

  const {
    status: wsStatus,
    data: wsData,
    send: wsSend,
    open: wsOpen,
    close: wsClose
  } = useWebSocket(wsUrl, {
    autoReconnect: true,
    immediate: false
  });

  /** reconnect 关闭当前连接后立刻用最新 token URL 重建。 应在用户登录成功后调用，确保 WebSocket 连接归属于当前登录用户。 */
  function reconnect() {
    wsClose();
    wsOpen();
  }

  const scrollToBottom = ref<null | (() => void)>(null);

  return {
    input,
    conversationId,
    list,
    wsStatus,
    wsData,
    wsSend,
    wsOpen,
    wsClose,
    reconnect,
    scrollToBottom
  };
});
