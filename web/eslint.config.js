import js from '@eslint/js';
import vue from 'eslint-plugin-vue';

export default [
  { ignores: ['dist/**', 'node_modules/**', '.tooling-check-*/**'] },
  js.configs.recommended,
  ...vue.configs['flat/recommended'],
  vue.configs['no-layout-rules'],
  {
    files: ['**/*.{js,mjs,vue}'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {
        __PIPED_DEVELOPMENT__: 'readonly',
        AbortController: 'readonly',
        console: 'readonly',
        document: 'readonly',
        fetch: 'readonly',
        File: 'readonly',
        navigator: 'readonly',
        Response: 'readonly',
        URL: 'readonly',
        URLSearchParams: 'readonly',
        window: 'readonly',
      },
    },
    rules: {
      curly: ['error', 'all'],
      eqeqeq: ['error', 'always'],
      'no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
      'vue/component-name-in-template-casing': ['error', 'PascalCase'],
      'vue/no-unused-properties': ['error', { groups: ['props'] }],
    },
  },
];
