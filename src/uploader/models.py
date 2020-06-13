from django.db import models


class Account(models.Model):
    iban = models.CharField(max_length=22, unique=True)


class Currency(models.Model):
    currency_short = models.CharField(max_length=3, unique=True)
    currency_long = models.CharField(max_length=100)


class OptioneeId(models.Model):
    optionee_id = models.CharField(max_length=26, unique=True)


class Optionee(models.Model):
    name = models.CharField(max_length=200)
    iban = models.CharField(max_length=22, unique=True)
    bic = models.CharField(max_length=11)
    optionee_id = models.ForeignKey(OptioneeId, on_delete=models.CASCADE)


class Transaction(models.Model):
    account = models.ForeignKey(Account, on_delete=models.CASCADE)
    date_booking = models.DateField()
    date_imported = models.DateField(auto_now_add=True)
    optionee_id = models.ForeignKey(OptioneeId, on_delete=models.CASCADE)
    optionee = models.ForeignKey(Optionee, on_delete=models.CASCADE)
    figure = models.DecimalField(max_digits=18, decimal_places=2)
    currency = models.ForeignKey(Currency, on_delete=models.CASCADE)
    reference = models.CharField(max_length=378)
