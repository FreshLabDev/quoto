import os
import unittest
from unittest.mock import AsyncMock

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from aiogram import types

import main
from app import i18n


class CommandScopeTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.bot = AsyncMock()
        await main.register_commands(self.bot)
        self.calls = [
            (
                call.kwargs["scope"],
                call.kwargs.get("language_code"),
                [command.command for command in call.args[0]],
                [command.description for command in call.args[0]],
            )
            for call in self.bot.set_my_commands.await_args_list
        ]

    def _for(self, scope_type: type, language: str | None):
        return [
            call
            for call in self.calls
            if isinstance(call[0], scope_type) and call[1] == language
        ]

    def test_start_is_the_only_registered_command_in_both_scopes(self) -> None:
        for scope in (
            types.BotCommandScopeDefault,
            types.BotCommandScopeAllPrivateChats,
            types.BotCommandScopeAllGroupChats,
        ):
            for _scope, _language, commands, _descriptions in self.calls:
                if isinstance(_scope, scope):
                    self.assertEqual(commands, ["start"])

    def test_privacy_is_no_longer_advertised(self) -> None:
        for _scope, _language, commands, _descriptions in self.calls:
            self.assertNotIn("privacy", commands)

    def test_every_supported_language_gets_both_scopes(self) -> None:
        for language in (None, *i18n.SUPPORTED_LANGUAGES):
            self.assertEqual(len(self._for(types.BotCommandScopeAllPrivateChats, language)), 1)
            self.assertEqual(len(self._for(types.BotCommandScopeAllGroupChats, language)), 1)

    def test_descriptions_are_localized_and_scope_specific(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            private = self._for(types.BotCommandScopeAllPrivateChats, language)[0]
            group = self._for(types.BotCommandScopeAllGroupChats, language)[0]
            self.assertEqual(private[3], [i18n.t(language, "command.start_private")])
            self.assertEqual(group[3], [i18n.t(language, "command.start_group")])
            self.assertNotEqual(private[3], group[3])

    def test_language_less_lists_are_english(self) -> None:
        private = self._for(types.BotCommandScopeAllPrivateChats, None)[0]
        self.assertEqual(private[3], [i18n.t(i18n.DEFAULT_LANGUAGE, "command.start_private")])

    def test_the_default_scope_is_overwritten_once(self) -> None:
        self.assertEqual(len(self._for(types.BotCommandScopeDefault, None)), 1)


if __name__ == "__main__":
    unittest.main()
